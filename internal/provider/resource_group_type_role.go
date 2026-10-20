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

// churchtools_group_type_role: the grants a role holds in EVERY group of its
// type — the `group_type_role` permission domain, addressed by the role's own id
// (the same id as churchtools_group_role_definition). churchtools_group_role is
// the per-group counterpart.
//
// Same grant blocks, same engine, same preserve_unknown as group_role. What
// differs, and is deliberate:
//
//   - the domain id is the role id, so nothing is resolved by name;
//   - CT accepts no type=revoke here ("revoke is only supported for
//     group_role"). A row is removed with DELETE and its exact authId, dataId
//     and isInherited — that is also all ct-cli does (applyPermissionPlan) —
//     and the resource never writes a deny row;
//   - like group_role, a row marked isInherited ("applies in subgroups") is a
//     row of its own and only ever revoked when it was in the plan: Read puts
//     every user-written row in state, so an undeclared one shows up as a
//     removal in the plan before an apply can touch it.
type groupTypeRoleResource struct{ client *client.Client }

type groupTypeRoleModel struct {
	ID              types.String `tfsdk:"id"`
	RoleID          types.String `tfsdk:"role_id"`
	GroupTypeID     types.String `tfsdk:"group_type_id"`
	Role            types.String `tfsdk:"role"`
	PreserveUnknown types.Set    `tfsdk:"preserve_unknown"`
	PreservedGrants types.Set    `tfsdk:"preserved_grants"`
	Grants          types.Set    `tfsdk:"grant"`
}

func (m *groupTypeRoleModel) data() *grantsData {
	return &grantsData{Grants: m.Grants, Preserve: m.PreserveUnknown, Preserved: m.PreservedGrants}
}

func (m *groupTypeRoleModel) setData(d *grantsData) {
	m.Grants, m.PreserveUnknown, m.PreservedGrants = d.Grants, d.Preserve, d.Preserved
}

func NewGroupTypeRoleResource() resource.Resource { return &groupTypeRoleResource{} }

var (
	_ resource.ResourceWithValidateConfig = &groupTypeRoleResource{}
	_ resource.ResourceWithImportState    = &groupTypeRoleResource{}
)

func (r *groupTypeRoleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_type_role"
}

func (r *groupTypeRoleResource) svc() grantsService {
	return grantsService{client: r.client, kind: groupTypeRoleKind}
}

func (r *groupTypeRoleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := grantsAttributes("type")
	attrs["id"] = schema.StringAttribute{
		Computed:      true,
		Description:   "The role id: the `group_type_role` permission domain.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["role_id"] = schema.StringAttribute{
		Required: true,
		Description: "Id of the role, e.g. churchtools_group_role_definition.x.id. A different role is a " +
			"different resource: changing it replaces this resource (the grants of the old role stay).",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
	attrs["group_type_id"] = schema.StringAttribute{
		Computed:      true,
		Description:   "The group type of the role, as read from ChurchTools.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["role"] = schema.StringAttribute{
		Computed:      true,
		Description:   "The role's name, as read from ChurchTools.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	resp.Schema = schema.Schema{
		Description: "Die Rechte einer Rolle auf Typebene: sie gelten in jeder Gruppe des Gruppentyps " +
			"(„Rechte verwalten“ an der Rolle des Gruppentyps). Jedes nicht deklarierte Recht wird entzogen, " +
			"außer auf den Dimensionen in preserve_unknown. applies_to_subgroups: das Recht gilt in den " +
			"Untergruppen jeder Gruppe des Typs.",
		Attributes: attrs,
		Blocks:     map[string]schema.Block{"grant": grantBlock(groupTypeRoleKind)},
	}
}

func (r *groupTypeRoleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *groupTypeRoleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m groupTypeRoleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.RoleID.IsUnknown() && !m.RoleID.IsNull() && !digitsOnly.MatchString(m.RoleID.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("role_id"), "Ungültige id",
			fmt.Sprintf("%q ist keine Rollen-id.", m.RoleID.ValueString()))
	}
	validatePreserve(m.PreserveUnknown, path.Root("preserve_unknown"), &resp.Diagnostics)
	validateGrants(ctx, groupTypeRoleKind, m.Grants, &resp.Diagnostics)
}

func (m *groupTypeRoleModel) label() string {
	return fmt.Sprintf("Rolle %q (%s) auf Typebene", m.Role.ValueString(), m.RoleID.ValueString())
}

// loadRole reads the role definition: its existence is the existence of the domain.
func (r *groupTypeRoleResource) loadRole(ctx context.Context, m *groupTypeRoleModel, id string) (found bool, err error) {
	row, err := r.client.Get(ctx, client.GroupRolesCollection, id)
	if isNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	m.ID = types.StringValue(id)
	m.GroupTypeID = types.StringValue(idString(row["groupTypeId"]))
	m.Role = types.StringValue(stringField(row, "name"))
	return true, nil
}

func (r *groupTypeRoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan groupTypeRoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const summary = "Rechte der Rolle konnten nicht gesetzt werden"
	id := plan.RoleID.ValueString()
	found, err := r.loadRole(ctx, &plan, id)
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	if !found {
		resp.Diagnostics.AddError(summary, fmt.Sprintf("Die Rolle %s existiert nicht in ChurchTools.", id))
		return
	}
	d := plan.data()
	if !r.svc().create(ctx, id, plan.label(), summary, d, &resp.Diagnostics) {
		return
	}
	plan.setData(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupTypeRoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state groupTypeRoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	found, err := r.loadRole(ctx, &state, id)
	if err != nil {
		resp.Diagnostics.AddError("Rechte der Rolle konnten nicht gelesen werden", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}
	state.RoleID = types.StringValue(id)
	d := state.data()
	ok, gone := r.svc().read(ctx, id, state.label(), "Rechte der Rolle konnten nicht gelesen werden", d, &resp.Diagnostics)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	if !ok {
		return
	}
	state.setData(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *groupTypeRoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state groupTypeRoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pd := plan.data()
	if !r.svc().update(ctx, state.ID.ValueString(), plan.label(), "Rechte der Rolle konnten nicht geändert werden",
		pd, state.data(), &resp.Diagnostics) {
		return
	}
	plan.setData(pd)
	plan.ID, plan.GroupTypeID, plan.Role = state.ID, state.GroupTypeID, state.Role
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete un-manages without revoking, like churchtools_group_role. Removing the
// grants is done by removing the grant blocks first; the plan shows every row.
func (r *groupTypeRoleResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	grantsDeleteWarning(&resp.Diagnostics, "dieser Rolle auf Typebene")
}

// ImportState takes the role id, or `<role id>/<dim>,<dim>` for preserve_unknown
// (import runs Read without the configuration, see parsePreserveSuffix).
func (r *groupTypeRoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if (len(parts) != 1 && len(parts) != 2) || !digitsOnly.MatchString(parts[0]) {
		resp.Diagnostics.AddError("Ungültige Import-id",
			fmt.Sprintf("%q: erwartet die Rollen-id, optional /<dimension>,<dimension> für preserve_unknown, "+
				"z. B. 160 oder 160/crp_query.", req.ID))
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
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("preserve_unknown"), preserve)...)
}
