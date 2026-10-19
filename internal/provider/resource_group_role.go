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
	validatePreserve(m.PreserveUnknown, path.Root("preserve_unknown"), &resp.Diagnostics)
	validateGrants(ctx, groupRoleKind, m.Grants, &resp.Diagnostics)
}

// --- pairing ---

var errGroupGone = errors.New("group gone")

// resolvePairing finds the (group, role) pairing id: by id when known (Read),
// by role name otherwise (Create, import). It returns errGroupGone for a group
// that 404s and "" for a pairing that no longer exists.
func (r *groupRoleResource) resolvePairing(ctx context.Context, groupID, role, id string) (string, string, error) {
	rows, err := r.client.GroupRoles(ctx, groupID)
	var se *client.StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return "", "", errGroupGone
	}
	if err != nil {
		return "", "", err
	}
	if id != "" {
		for _, row := range rows {
			if idString(row["id"]) == id {
				return id, stringField(row, "name"), nil
			}
		}
		return "", "", nil
	}
	var names []string
	var match []string
	for _, row := range rows {
		names = append(names, strconv.Quote(stringField(row, "name")))
		if stringField(row, "name") == role {
			match = append(match, idString(row["id"]))
		}
	}
	switch len(match) {
	case 1:
		if match[0] == "" {
			return "", "", fmt.Errorf("die Rolle %q der Gruppe %s hat keine id", role, groupID)
		}
		return match[0], role, nil
	case 0:
		sort.Strings(names)
		return "", "", fmt.Errorf("die Gruppe %s hat keine Rolle %q (vorhanden: %s). Der Name muss "+
			"dem Feld name in ChurchTools entsprechen, nicht der Übersetzung", groupID, role, strings.Join(names, ", "))
	default:
		return "", "", fmt.Errorf("die Gruppe %s hat %d Rollen namens %q", groupID, len(match), role)
	}
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
	id, _, err := r.resolvePairing(ctx, plan.GroupID.ValueString(), plan.Role.ValueString(), "")
	if errors.Is(err, errGroupGone) {
		err = fmt.Errorf("die Gruppe %s existiert nicht", plan.GroupID.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	d := plan.data()
	if !r.svc().create(ctx, id, plan.label(plan.Role.ValueString()), summary, d, &resp.Diagnostics) {
		return
	}
	plan.setData(d)
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
	id, role, err := r.resolvePairing(ctx, state.GroupID.ValueString(), state.Role.ValueString(), state.ID.ValueString())
	if errors.Is(err, errGroupGone) || (err == nil && id == "") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Rollenrechte konnten nicht gelesen werden", err.Error())
		return
	}
	d := state.data()
	if !r.svc().read(ctx, id, state.label(role), "Rollenrechte konnten nicht gelesen werden", d, &resp.Diagnostics) {
		return
	}
	state.setData(d)
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
	plan.setData(pd)
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
