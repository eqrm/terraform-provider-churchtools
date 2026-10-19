package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// churchtools_person_status_grants: the rights a person status carries — the
// `status` permission domain, addressed by the status id (0 is "Unbekannt" on
// both eqrm hosts). The one right ct-cli declares there is the SSO login:
// `churchcore:login to external system`, scope oauth_client -1 (all).
//
// It is a separate resource from churchtools_person_status on purpose: the
// status row is master data, the grants are a permission domain, and tier 2
// already keeps those apart. CT allows no inheritance on this domain, so the
// grant block has no applies_to_subgroups, and only authIds below 10000.
//
// Handle with care: that login right is what lets every person with an account
// sign in through the external login. A mis-read -1 would revoke it
// instance-wide, so the plan must be empty after import before anything else.
type personStatusGrantsResource struct{ client *client.Client }

type personStatusGrantsModel struct {
	ID              types.String `tfsdk:"id"`
	PersonStatusID  types.String `tfsdk:"person_status_id"`
	PreserveUnknown types.Set    `tfsdk:"preserve_unknown"`
	PreservedGrants types.Set    `tfsdk:"preserved_grants"`
	Grants          types.Set    `tfsdk:"grant"`
}

func (m *personStatusGrantsModel) data() *grantsData {
	return &grantsData{Grants: m.Grants, Preserve: m.PreserveUnknown, Preserved: m.PreservedGrants}
}

func (m *personStatusGrantsModel) setData(d *grantsData) {
	m.Grants, m.PreserveUnknown, m.PreservedGrants = d.Grants, d.Preserve, d.Preserved
}

func (m *personStatusGrantsModel) label() string {
	return fmt.Sprintf("Personenstatus %s", m.PersonStatusID.ValueString())
}

func NewPersonStatusGrantsResource() resource.Resource { return &personStatusGrantsResource{} }

var (
	_ resource.ResourceWithValidateConfig = &personStatusGrantsResource{}
	_ resource.ResourceWithImportState    = &personStatusGrantsResource{}
)

func (r *personStatusGrantsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_person_status_grants"
}

func (r *personStatusGrantsResource) svc() grantsService {
	return grantsService{client: r.client, kind: statusKind}
}

func (r *personStatusGrantsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := grantsAttributes("status")
	attrs["id"] = schema.StringAttribute{
		Computed:      true,
		Description:   "The person status id: the `status` permission domain.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["person_status_id"] = schema.StringAttribute{
		Required:      true,
		Description:   "Id of the person status, e.g. churchtools_person_status.x.id (0 = Unbekannt).",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
	resp.Schema = schema.Schema{
		Description: "Die Rechte eines Personenstatus (z. B. des Status „Unbekannt“ mit dem Recht auf den " +
			"SSO-Login). Jedes nicht deklarierte Recht wird entzogen, außer auf den Dimensionen in " +
			"preserve_unknown. Vererbung gibt es an dieser Domäne nicht.",
		Attributes: attrs,
		Blocks:     map[string]schema.Block{"grant": grantBlock(statusKind)},
	}
}

func (r *personStatusGrantsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *personStatusGrantsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m personStatusGrantsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.PersonStatusID.IsUnknown() && !m.PersonStatusID.IsNull() && !roleIDPattern.MatchString(m.PersonStatusID.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("person_status_id"), "Ungültige id",
			fmt.Sprintf("%q ist keine Personenstatus-id.", m.PersonStatusID.ValueString()))
	}
	validatePreserve(m.PreserveUnknown, path.Root("preserve_unknown"), &resp.Diagnostics)
	validateGrants(ctx, statusKind, m.Grants, &resp.Diagnostics)
}

func (r *personStatusGrantsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan personStatusGrantsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const summary = "Rechte des Personenstatus konnten nicht gesetzt werden"
	// The domain exists exactly as long as the status does; a 404 here is a
	// status that is not there, and the apply reports it as such.
	if _, err := r.client.PermissionRows(ctx, statusKind.domain, plan.PersonStatusID.ValueString()); err != nil {
		if isNotFound(err) {
			err = fmt.Errorf("der Personenstatus %s existiert nicht in ChurchTools", plan.PersonStatusID.ValueString())
		}
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	d := plan.data()
	if !r.svc().create(ctx, plan.PersonStatusID.ValueString(), plan.label(), summary, d, &resp.Diagnostics) {
		return
	}
	plan.setData(d)
	plan.ID = plan.PersonStatusID
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *personStatusGrantsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state personStatusGrantsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d := state.data()
	ok, gone := r.svc().read(ctx, state.PersonStatusID.ValueString(), state.label(),
		"Rechte des Personenstatus konnten nicht gelesen werden", d, &resp.Diagnostics)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	if !ok {
		return
	}
	state.setData(d)
	state.ID = state.PersonStatusID
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *personStatusGrantsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state personStatusGrantsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pd := plan.data()
	if !r.svc().update(ctx, state.PersonStatusID.ValueString(), plan.label(),
		"Rechte des Personenstatus konnten nicht geändert werden", pd, state.data(), &resp.Diagnostics) {
		return
	}
	plan.setData(pd)
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete un-manages without revoking. This domain carries the SSO login right:
// removing the resource must never silently take it away.
func (r *personStatusGrantsResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	grantsDeleteWarning(&resp.Diagnostics, "dieses Personenstatus")
}

// ImportState takes the status id, or `<status id>/<dim>,<dim>` for preserve_unknown.
func (r *personStatusGrantsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if (len(parts) != 1 && len(parts) != 2) || !roleIDPattern.MatchString(parts[0]) {
		resp.Diagnostics.AddError("Ungültige Import-id",
			fmt.Sprintf("%q: erwartet die Personenstatus-id, optional /<dimension>,<dimension> für "+
				"preserve_unknown, z. B. 0.", req.ID))
		return
	}
	preserve := types.SetNull(types.StringType)
	if len(parts) == 2 {
		preserve = parsePreserveSuffix(parts[1], &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("person_status_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("preserve_unknown"), preserve)...)
}
