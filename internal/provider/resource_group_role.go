package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// churchtools_group_role: the grants one role holds in one group — the
// `group_role` permission domain, addressed by CT's (group, role) pairing id.
//
// It does not create anything in ChurchTools. CT creates a pairing for every
// role of the group's type when the group is created, so Create resolves the
// pairing by role name and writes the declared grants onto it.
//
// grow_path_id is the one per-group setting here that is not a grant: the
// Wachstumspfad stage of this role in this group, stored on the same
// /groups/{id}/roles row and written with PATCH /groups/{id}/roles/{roleId}
// (partial: only growPathId is sent, the row's other settings are untouched).
// Like group_role_definition's it is Optional+Computed: managed only when set,
// the live value kept and never compared when left out, and removing it does not
// clear a stage (see the attribute description).
//
// The hard part is preserve_unknown (eqrm/ct-cli#102, spec "preserveUnknown"):
// Terraform revokes whatever is in state and not in config, and has no notion of
// a partially managed list. This resource gets partial ownership by keeping the
// rows it must not own out of STATE — see filterLiveRows — and by never sending
// a DELETE for a row that was not in state (planGrantWrites). A row that is not
// in state cannot be in a plan, so it cannot be revoked by an apply.
type groupRoleResource struct {
	client *client.Client
}

type groupRoleModel struct {
	ID              types.String `tfsdk:"id"`
	GroupID         types.String `tfsdk:"group_id"`
	Role            types.String `tfsdk:"role"`
	PreserveUnknown types.Set    `tfsdk:"preserve_unknown"`
	PreservedGrants types.Set    `tfsdk:"preserved_grants"`
	Grants          types.Set    `tfsdk:"grant"`
	GrowPathID      types.Int64  `tfsdk:"grow_path_id"`
	ClearGrowPath   types.Bool   `tfsdk:"clear_grow_path"`
}

func (m *groupRoleModel) data() *grantsData {
	return &grantsData{Grants: m.Grants, Preserve: m.PreserveUnknown, Preserved: m.PreservedGrants}
}

func (m *groupRoleModel) setData(d *grantsData) {
	m.Grants, m.PreserveUnknown, m.PreservedGrants = d.Grants, d.Preserve, d.Preserved
}

func (m *groupRoleModel) label(role string) string {
	return fmt.Sprintf("Rolle %q in Gruppe %s", role, m.GroupID.ValueString())
}

func NewGroupRoleResource() resource.Resource { return &groupRoleResource{} }

var (
	_ resource.ResourceWithValidateConfig = &groupRoleResource{}
	_ resource.ResourceWithImportState    = &groupRoleResource{}
)

func (r *groupRoleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_role"
}

func (r *groupRoleResource) svc() grantsService {
	return grantsService{client: r.client, kind: groupRoleKind}
}

func (r *groupRoleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := grantsAttributes("group")
	attrs["id"] = schema.StringAttribute{
		Computed:      true,
		Description:   "The (group, role) pairing id: the `group_role` permission domain.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
	attrs["group_id"] = schema.StringAttribute{
		Required:      true,
		Description:   "Id of the group, e.g. churchtools_group.x.id.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
	attrs["role"] = schema.StringAttribute{
		Required: true,
		Description: "Role name as ChurchTools stores it (`name` on /groups/{id}/roles, e.g. " +
			"\"Mitglied\", \"Leiter\", \"participant\"), not the translated label.",
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
	attrs["grow_path_id"] = schema.Int64Attribute{
		Optional: true, Computed: true,
		Description: "Id of the growth path stage (churchtools_grow_path.x.id as a number) that members of " +
			"this role reach in this group only; the type-level default is group_role_definition.grow_path_id. " +
			"ChurchTools picks the stage with the higher sort_key among a person's roles. Left out: the live " +
			"value is kept (null for none) and never compared. Removing the attribute does not clear a stage " +
			"that is set, and null cannot be configured; use clear_grow_path = true to clear it.",
		PlanModifiers: []planmodifier.Int64{growPathPlan()},
	}
	attrs["clear_grow_path"] = clearGrowPathAttribute()
	resp.Schema = schema.Schema{
		Description: "Die Rechte einer Rolle in einer Gruppe („Rechte verwalten“ an der Gruppe). " +
			"Jedes nicht deklarierte Recht wird entzogen, außer auf den Dimensionen in preserve_unknown.",
		Attributes: attrs,
		Blocks:     map[string]schema.Block{"grant": grantBlock(groupRoleKind)},
	}
}

func (r *groupRoleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

// ValidateConfig catches at plan time what would otherwise be a silent wrong
// grant; see validateGrants.
func (r *groupRoleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m groupRoleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateGrowPathClear(m.GrowPathID, m.ClearGrowPath, &resp.Diagnostics)
	validatePreserve(m.PreserveUnknown, path.Root("preserve_unknown"), &resp.Diagnostics)
	validateGrants(ctx, groupRoleKind, m.Grants, &resp.Diagnostics)
}

// --- pairing ---

var errGroupGone = errors.New("group gone")

// pairing is one /groups/{id}/roles row, reduced to what this resource uses.
type pairing struct {
	id, name string
	growPath types.Int64
}

func pairingOf(row map[string]any) pairing {
	p := pairing{id: idString(row["id"]), name: stringField(row, "name"), growPath: types.Int64Null()}
	if _, ok := row["growPathId"].(float64); ok {
		p.growPath = types.Int64Value(intField(row, "growPathId"))
	}
	return p
}

// resolvePairing finds the (group, role) pairing: by id when known (Read),
// by role name otherwise (Create, import). It returns errGroupGone for a group
// that 404s and a zero pairing (id "") for one that no longer exists.
func (r *groupRoleResource) resolvePairing(ctx context.Context, groupID, role, id string) (pairing, error) {
	rows, err := r.client.GroupRoles(ctx, groupID)
	var se *client.StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return pairing{}, errGroupGone
	}
	if err != nil {
		return pairing{}, err
	}
	if id != "" {
		for _, row := range rows {
			if idString(row["id"]) == id {
				return pairingOf(row), nil
			}
		}
		return pairing{}, nil
	}
	var names []string
	var match []pairing
	for _, row := range rows {
		names = append(names, strconv.Quote(stringField(row, "name")))
		if stringField(row, "name") == role {
			match = append(match, pairingOf(row))
		}
	}
	switch len(match) {
	case 1:
		if match[0].id == "" {
			return pairing{}, fmt.Errorf("die Rolle %q der Gruppe %s hat keine id", role, groupID)
		}
		return match[0], nil
	case 0:
		sort.Strings(names)
		return pairing{}, fmt.Errorf("die Gruppe %s hat keine Rolle %q (vorhanden: %s). Der Name muss "+
			"dem Feld name in ChurchTools entsprechen, nicht der Übersetzung", groupID, role, strings.Join(names, ", "))
	default:
		return pairing{}, fmt.Errorf("die Gruppe %s hat %d Rollen namens %q", groupID, len(match), role)
	}
}

// writeGrowPath PATCHes growPathId when the plan sets a value that differs from
// the live one, then reads the row back: only the read-back proves what CT
// stored. It returns the live value for state. Unset or equal: no request.
func (r *groupRoleResource) writeGrowPath(ctx context.Context, m groupRoleModel, p pairing, summary string, diags *diag.Diagnostics) (types.Int64, bool) {
	want := m.GrowPathID
	var value any = want.ValueInt64()
	if m.ClearGrowPath.ValueBool() {
		want, value = types.Int64Null(), nil
	} else if want.IsUnknown() || want.IsNull() {
		return p.growPath, true
	}
	if p.growPath.Equal(want) {
		return p.growPath, true
	}
	label := m.label(m.Role.ValueString())
	if err := r.client.PatchGroupRole(ctx, m.GroupID.ValueString(), p.id, client.Row{"growPathId": value}); err != nil {
		diags.AddError(summary, fmt.Sprintf("%s: %v", label, err))
		return types.Int64Null(), false
	}
	back, err := r.resolvePairing(ctx, m.GroupID.ValueString(), "", p.id)
	if err == nil && back.id == "" {
		err = errors.New("die Rolle ist nach dem Schreiben nicht mehr lesbar")
	}
	if err != nil {
		diags.AddError(summary, fmt.Sprintf("%s: %v", label, err))
		return types.Int64Null(), false
	}
	if !back.growPath.Equal(want) {
		diags.AddError(summary, fmt.Sprintf("%s: grow_path_id %s wurde geschrieben, ChurchTools liefert %s zurück",
			label, want, back.growPath))
		return types.Int64Null(), false
	}
	return back.growPath, true
}

// --- CRUD ---

func (r *groupRoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan groupRoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const summary = "Rollenrechte konnten nicht gesetzt werden"
	p, err := r.resolvePairing(ctx, plan.GroupID.ValueString(), plan.Role.ValueString(), "")
	if errors.Is(err, errGroupGone) {
		err = fmt.Errorf("die Gruppe %s existiert nicht", plan.GroupID.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	id := p.id
	gp, ok := r.writeGrowPath(ctx, plan, p, summary, &resp.Diagnostics)
	if !ok {
		return
	}
	d := plan.data()
	if !r.svc().create(ctx, id, plan.label(plan.Role.ValueString()), summary, d, &resp.Diagnostics) {
		return
	}
	plan.setData(d)
	plan.GrowPathID = gp
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupRoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state groupRoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.resolvePairing(ctx, state.GroupID.ValueString(), state.Role.ValueString(), state.ID.ValueString())
	id, role := p.id, p.name
	if errors.Is(err, errGroupGone) || (err == nil && id == "") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Rollenrechte konnten nicht gelesen werden", err.Error())
		return
	}
	d := state.data()
	ok, gone := r.svc().read(ctx, id, state.label(role), "Rollenrechte konnten nicht gelesen werden", d, &resp.Diagnostics)
	if gone {
		resp.State.RemoveResource(ctx)
		return
	}
	if !ok {
		return
	}
	state.setData(d)
	state.GrowPathID = p.growPath
	state.ID = types.StringValue(id)
	state.Role = types.StringValue(role)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *groupRoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state groupRoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pd := plan.data()
	if !r.svc().update(ctx, state.ID.ValueString(), plan.label(plan.Role.ValueString()),
		"Rollenrechte konnten nicht geändert werden", pd, state.data(), &resp.Diagnostics) {
		return
	}
	const summary = "Rollenrechte konnten nicht geändert werden"
	p, err := r.resolvePairing(ctx, plan.GroupID.ValueString(), plan.Role.ValueString(), state.ID.ValueString())
	if err == nil && p.id == "" {
		err = errors.New("die Rolle existiert nicht mehr")
	}
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	gp, ok := r.writeGrowPath(ctx, plan, p, summary, &resp.Diagnostics)
	if !ok {
		return
	}
	plan.setData(pd)
	plan.GrowPathID = gp
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete un-manages without revoking, like every resource here and like
// removing a declaration in ct-cli. Revoking a role's grants is done by
// removing the grant blocks first, which the plan shows row by row.
func (r *groupRoleResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	grantsDeleteWarning(&resp.Diagnostics, "dieser Rolle")
}

// ImportState takes `<group_id>/<role>` or `<group_id>/<role>/<dim>,<dim>`.
//
// The third part exists because import runs Read without the configuration:
// without it, an imported role would first hold its preserved rows in state,
// and the first plan would show them as removals.
func (r *groupRoleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if (len(parts) != 2 && len(parts) != 3) || !digitsOnly.MatchString(parts[0]) || parts[1] == "" {
		resp.Diagnostics.AddError("Ungültige Import-id",
			fmt.Sprintf("%q: erwartet <gruppen-id>/<rollenname>, optional /<dimension>,<dimension> für "+
				"preserve_unknown, z. B. 1748/Mitglied oder 1748/Mitglied/crp_query.", req.ID))
		return
	}
	preserve := types.SetNull(types.StringType)
	if len(parts) == 3 {
		preserve = parsePreserveSuffix(parts[2], &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("preserve_unknown"), preserve)...)
}
