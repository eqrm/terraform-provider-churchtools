package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Privacy-policy agreement types ("how was the privacy policy agreed to":
// Check-in, Gruppenanmeldung, a custom "Connect-App", …) have NO REST endpoint.
//
//   - Reads:  getMasterData → the row set `privacy_policy_agreement_types`,
//     keyed by id, every value a string. Read filters it by id.
//   - Writes: saveMasterData on `cdb_privacy_policy_agreement_types`, the call
//     the admin UI makes. It returns NO id, so Create snapshots the ids before
//     writing and diffs after, exactly like the department resource.
//
// Columns: id, bezeichnung, deletable, sortkey (verified on both eqrm hosts via
// getMasterData's masterDataTables). `deletable` is ChurchTools' own guard (0 on
// the built-ins) and is exposed read-only; this provider never sets it.
const privacyAgreementTypesKey = "privacy_policy_agreement_types"

var privacyAgreementColumns = map[string]string{
	"name":    "bezeichnung",
	"sortKey": "sortkey",
}

type privacyAgreementTypeResource struct{ client *client.Client }

type privacyAgreementTypeModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	SortKey   types.Int64  `tfsdk:"sort_key"`
	Deletable types.Bool   `tfsdk:"deletable"`
}

func NewPrivacyAgreementTypeResource() resource.Resource { return &privacyAgreementTypeResource{} }

func (r *privacyAgreementTypeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_privacy_agreement_type"
}

func (r *privacyAgreementTypeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine ChurchTools Datenschutz-Zustimmungsart. ChurchTools bietet dafür keinen REST-Weg — " +
			"Lesen und Schreiben laufen über die Legacy-Stammdaten-Schnittstelle. Gelöscht wird nie.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// The stored `bezeichnung`: a translation key on the built-ins
			// ("privacy.policy.agreement.type.checkin"), plain text on custom rows.
			// Renaming a built-in would break its translation — keep the key.
			"name": schema.StringAttribute{Required: true},
			// Optional+Computed with no static default, for the reason given on
			// department.sort_key: omitting it keeps whatever ChurchTools has.
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"deletable": schema.BoolAttribute{Computed: true},
		},
	}
}

func (r *privacyAgreementTypeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *privacyAgreementTypeResource) legacyRow(m privacyAgreementTypeModel) map[string]any {
	return map[string]any{
		privacyAgreementColumns["name"]:    m.Name.ValueString(),
		privacyAgreementColumns["sortKey"]: m.SortKey.ValueInt64(),
	}
}

// legacyInt parses the legacy payload's stringly numbers, tolerating real ones.
func legacyInt(v any) (int64, bool) {
	switch n := v.(type) {
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	case float64:
		return int64(n), true
	}
	return 0, false
}

func (r *privacyAgreementTypeResource) fromRow(row map[string]any, m *privacyAgreementTypeModel) {
	m.Name = types.StringValue(stringField(row, "bezeichnung"))
	sortKey, _ := legacyInt(row["sortkey"])
	m.SortKey = types.Int64Value(sortKey)
	deletable, _ := legacyInt(row["deletable"])
	m.Deletable = types.BoolValue(deletable == 1)
}

func (r *privacyAgreementTypeResource) rows(ctx context.Context) (map[string]map[string]any, error) {
	return r.client.MasterDataRows(ctx, privacyAgreementTypesKey)
}

func (r *privacyAgreementTypeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan privacyAgreementTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// The admin UI pre-fills sortkey 0 for a new row; do the same.
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	defer r.client.LockMasterDataCreate()()
	before, err := r.rows(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Zustimmungsarten konnten nicht gelesen werden", err.Error())
		return
	}
	// Refuse a same-name create: saveMasterData returns no id, so a second row of
	// the same name could not be told from the first (see department Create).
	name := plan.Name.ValueString()
	for id, row := range before {
		if stringField(row, "bezeichnung") == name {
			resp.Diagnostics.AddError(
				"Zustimmungsart existiert bereits",
				fmt.Sprintf("In ChurchTools gibt es bereits eine Zustimmungsart %q (#%s). Es wird keine zweite "+
					"angelegt: die Legacy-Schnittstelle liefert keine id zurück. Importiere sie stattdessen: "+
					"tofu import churchtools_privacy_agreement_type.<name> %s", name, id, id))
			return
		}
	}
	if err := r.client.SaveMasterData(ctx, client.PrivacyAgreementTypesTable, r.legacyRow(plan), nil); err != nil {
		resp.Diagnostics.AddError("Zustimmungsart konnte nicht angelegt werden", err.Error())
		return
	}
	after, err := r.rows(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Zustimmungsart wurde angelegt, konnte aber nicht gelesen werden", err.Error())
		return
	}
	fresh := make([]string, 0, 1)
	for id := range after {
		if _, known := before[id]; !known {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) != 1 {
		resp.Diagnostics.AddError(
			"Zustimmungsart angelegt, id unbestimmbar",
			fmt.Sprintf("Die Zustimmungsart %q wurde angelegt, aber getMasterData zeigt %d neue Zeilen statt "+
				"genau einer — die id lässt sich nicht zuordnen. Prüfe ChurchTools auf eine verwaiste Zeile, "+
				"bevor du es erneut versuchst.", name, len(fresh)))
		return
	}
	plan.ID = types.StringValue(fresh[0])
	// Keep the planned name and sort_key, as department does: copying back what
	// the legacy write stored would turn any server-side normalisation into
	// "inconsistent result after apply" -- with the row already created. Only
	// the column this provider never writes comes from ChurchTools.
	deletable, _ := legacyInt(after[fresh[0]]["deletable"])
	plan.Deletable = types.BoolValue(deletable == 1)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *privacyAgreementTypeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state privacyAgreementTypeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rows, err := r.rows(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Zustimmungsarten konnten nicht gelesen werden", err.Error())
		return
	}
	row, ok := rows[state.ID.ValueString()]
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	r.fromRow(row, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *privacyAgreementTypeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state privacyAgreementTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	id, err := strconv.Atoi(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Ungültige Zustimmungsart-id", fmt.Sprintf("%q ist keine Zahl", state.ID.ValueString()))
		return
	}
	// A non-empty id makes the same legacy call an UPDATE of that row.
	if err := r.client.SaveMasterData(ctx, client.PrivacyAgreementTypesTable, r.legacyRow(plan), &id); err != nil {
		resp.Diagnostics.AddError("Zustimmungsart konnte nicht aktualisiert werden", err.Error())
		return
	}
	plan.Deletable = state.Deletable
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete orphans, like every resource here. ChurchTools DOES have a legacy delete
// (the admin UI calls canDeleteMasterData, then deleteMasterData — assets.js
// deleteMasterData()); this provider deliberately never uses it.
func (r *privacyAgreementTypeResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Datenschutz-Zustimmungsarten"))
}

func (r *privacyAgreementTypeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
