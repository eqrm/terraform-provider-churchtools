package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Bereiche are the one tier-0 type with NO REST write path.
//
//   - Reads:  GET /departments — the whole collection, and the only read CT
//     offers. There is no GET /departments/{id}, so Read filters the list.
//   - Writes: the legacy master-data endpoint (churchdb/ajax, cdb_bereich),
//     which returns NO id. Create therefore snapshots the ids before writing
//     and diffs after, exactly as ct-cli does.
//
// An earlier version of this resource used POST/PUT /departments. It passed
// CI only because the mock answered every verb on every path; against a live
// instance it would have failed on the first apply. The mock's supportedVerbs
// allowlist exists to make that class of mistake impossible to merge.
const departmentCollection = "/departments"

// The config speaks the REST names; the legacy table speaks its own. Mapping
// here keeps that detail off the config surface, and SaveMasterData validates
// the result against the instance's own column list.
var departmentColumns = map[string]string{
	"name":    "bezeichnung",
	"shorty":  "kuerzel",
	"sortKey": "sortkey",
}

type departmentResource struct{ client *client.Client }

type departmentModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Shorty  types.String `tfsdk:"shorty"`
	SortKey types.Int64  `tfsdk:"sort_key"`
}

func NewDepartmentResource() resource.Resource { return &departmentResource{} }

func (r *departmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_department"
}

func (r *departmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ein ChurchTools Bereich. ChurchTools bietet für Bereiche keinen REST-Schreibweg — " +
			"Änderungen laufen über die Legacy-Stammdaten-Schnittstelle. Gelöscht wird nie.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name":   schema.StringAttribute{Required: true},
			"shorty": schema.StringAttribute{Required: true},
			// ct-cli stores this as `sortKey ?? 0`; matching that keeps an
			// exported resource that omits it a no-op rather than a diff.
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(0),
			},
		},
	}
}

func (r *departmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

func (r *departmentResource) legacyRow(m departmentModel) map[string]any {
	return map[string]any{
		departmentColumns["name"]:    m.Name.ValueString(),
		departmentColumns["shorty"]:  m.Shorty.ValueString(),
		departmentColumns["sortKey"]: m.SortKey.ValueInt64(),
	}
}

// findByID filters the collection read, because CT serves no /departments/{id}.
func (r *departmentResource) findByID(ctx context.Context, id string) (client.Row, error) {
	rows, err := r.client.List(ctx, departmentCollection)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if idString(row["id"]) == id {
			return row, nil
		}
	}
	return nil, nil
}

func (r *departmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan departmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	before, err := r.client.List(ctx, departmentCollection)
	if err != nil {
		resp.Diagnostics.AddError("Bereiche konnten nicht gelesen werden", err.Error())
		return
	}

	// Refuse a same-name create rather than write one we cannot identify
	// afterwards. saveMasterData returns no id, so a second Bereich of the same
	// name could not be told from the first — and the create would already have
	// succeeded, leaving an orphan that every retry duplicates.
	name := plan.Name.ValueString()
	known := make(map[string]bool, len(before))
	for _, row := range before {
		known[idString(row["id"])] = true
		if stringField(row, "name") == name {
			resp.Diagnostics.AddError(
				"Bereich existiert bereits",
				fmt.Sprintf("In ChurchTools gibt es bereits einen Bereich %q (#%s). Es wird kein zweiter "+
					"angelegt: die Legacy-Schnittstelle liefert keine id zurück, der neue Datensatz wäre "+
					"vom bestehenden nicht zu unterscheiden. Importiere ihn stattdessen: "+
					"tofu import churchtools_department.<name> %s",
					name, idString(row["id"]), idString(row["id"])))
			return
		}
	}

	if err := r.client.SaveMasterData(ctx, client.DepartmentTable, r.legacyRow(plan), nil); err != nil {
		resp.Diagnostics.AddError("Bereich konnte nicht angelegt werden", err.Error())
		return
	}

	after, err := r.client.List(ctx, departmentCollection)
	if err != nil {
		resp.Diagnostics.AddError("Bereich wurde angelegt, konnte aber nicht gelesen werden", err.Error())
		return
	}
	fresh := make([]string, 0, 1)
	for _, row := range after {
		if id := idString(row["id"]); !known[id] {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) != 1 {
		resp.Diagnostics.AddError(
			"Bereich angelegt, id unbestimmbar",
			fmt.Sprintf("Der Bereich %q wurde angelegt, aber GET /departments zeigt %d neue Zeilen statt "+
				"genau einer — die id lässt sich nicht zuordnen. Prüfe ChurchTools auf einen verwaisten "+
				"Bereich, bevor du es erneut versuchst.", name, len(fresh)))
		return
	}

	plan.ID = types.StringValue(fresh[0])
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *departmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state departmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.findByID(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Bereiche konnten nicht gelesen werden", err.Error())
		return
	}
	if row == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Name = types.StringValue(stringField(row, "name"))
	state.Shorty = types.StringValue(stringField(row, "shorty"))
	state.SortKey = types.Int64Value(intField(row, "sortKey"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *departmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state departmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID

	id, err := strconv.Atoi(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Ungültige Bereichs-id", fmt.Sprintf("%q ist keine Zahl", state.ID.ValueString()))
		return
	}
	// A non-empty id makes the same legacy call an UPDATE of that row.
	if err := r.client.SaveMasterData(ctx, client.DepartmentTable, r.legacyRow(plan), &id); err != nil {
		resp.Diagnostics.AddError("Bereich konnte nicht aktualisiert werden", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *departmentResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Bereiche"))
}

func (r *departmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
