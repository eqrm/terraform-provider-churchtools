package provider

import (
	"context"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Privacy-policy agreement "who" ("who agreed": the person themselves, their
// parents, whoever signed them up for a group). Same legacy table family as
// privacy_policy_agreement_types, read through getMasterData.
//
// IMPORT-ONLY, and deliberately so. Unlike the types — where eqrm production
// carries a custom "Connect-App" row that the dev apply has to create — every
// row here is a ChurchTools BUILT-IN, and the set is byte-identical on both
// eqrm hosts (verified 2026-09-26 via getMasterData):
//
//	1     privacy.policy.agreement.who.own
//	2     privacy.policy.agreement.who.parents
//	10000 privacy.policy.agreement.who.groupsignupperson
//
// So there is nothing to create, and Create refuses rather than pretending.
// That keeps `cdb_privacy_policy_agreement_who` OFF the client's write
// allowlist: the allowlist is a safety boundary, not a convenience, and there
// is no reason to widen it for a table this provider will never write.
//
// It is a managed resource rather than a data source because the id map is
// built from `mode == "managed"` rows of the tofu state (ct-structure#121); a
// data source would read fine and then be dropped from `.ct/ids.<host>.json`.
const privacyAgreementWhoKey = "privacy_policy_agreement_who"

type privacyAgreementWhoResource struct{ client *client.Client }

type privacyAgreementWhoModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	SortKey types.Int64  `tfsdk:"sort_key"`
}

func NewPrivacyAgreementWhoResource() resource.Resource { return &privacyAgreementWhoResource{} }

func (r *privacyAgreementWhoResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_privacy_agreement_who"
}

func (r *privacyAgreementWhoResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine ChurchTools Datenschutz-Zustimmung-durch-Art (wer zugestimmt hat). " +
			"Nur importierbar: alle Zeilen sind ChurchTools-Built-ins und auf jeder Instanz identisch.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// Computed, not Required: the name is a translation key owned by
			// ChurchTools. Exposing it read-only still surfaces drift if an
			// instance ever renames one, without inviting a config to fight it.
			"name":     schema.StringAttribute{Computed: true},
			"sort_key": schema.Int64Attribute{Computed: true},
		},
	}
}

func (r *privacyAgreementWhoResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *privacyAgreementWhoResource) rows(ctx context.Context) (map[string]map[string]any, error) {
	return r.client.MasterDataRows(ctx, privacyAgreementWhoKey)
}

// The not-configured check comes FIRST, before the import-only refusal: a
// provider with no host is a different problem from asking for an unsupported
// create, and naming the wrong one sends the reader to the wrong file.
func (r *privacyAgreementWhoResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.AddError(
		"Zustimmung-durch-Art kann nicht angelegt werden",
		"Alle Zustimmung-durch-Arten sind ChurchTools-Built-ins (1 own, 2 parents, 10000 groupsignupperson) "+
			"und existieren auf jeder Instanz. Dieser Provider legt keine an — importiere die vorhandene "+
			"Zeile stattdessen, z. B.:\n\n"+
			"  import {\n    to = churchtools_privacy_agreement_who.<name>\n    id = \"1\"\n  }")
}

func (r *privacyAgreementWhoResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state privacyAgreementWhoModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rows, err := r.rows(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Zustimmung-durch-Arten konnten nicht gelesen werden", err.Error())
		return
	}
	row, ok := rows[state.ID.ValueString()]
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Name = types.StringValue(stringField(row, "bezeichnung"))
	sortKey, _ := legacyInt(row["sortkey"])
	state.SortKey = types.Int64Value(sortKey)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update cannot be reached: every attribute is Computed, so no config change can
// produce a diff. It exists because resource.Resource requires it.
func (r *privacyAgreementWhoResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state privacyAgreementWhoModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *privacyAgreementWhoResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Zustimmung-durch-Arten"))
}

func (r *privacyAgreementWhoResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
