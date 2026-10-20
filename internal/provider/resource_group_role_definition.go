package provider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// churchtools_group_role_definition: a role of a group type (Mitglied, Leiter,
// ...) — `/group/roles`. Its id is also the id of the role's type-level
// permission domain (churchtools_group_type_role) and what memberships, the
// connector and dynamic-group rulesets point at.
//
// That is why this resource is stricter than the master-data resources here:
//
//   - it never replaces. Deleting and re-creating a role is a new id: the
//     type-level grants, the per-group pairings (and their grants), every
//     membership and all references by id are gone or dangling. The one
//     attribute that would force a replacement, group_type_id, is a plan-time
//     error instead; CT does not move a role between types either.
//   - Delete only un-manages, like the other resources: the role is dropped from
//     state with a warning and never deleted in CT (see Delete).
//   - PUT is not partial: Update reads the live role, overlays the planned
//     values and sends the whole object, so shorty and growPathId that this
//     configuration does not manage survive.
type groupRoleDefinitionResource struct{ client *client.Client }

type groupRoleDefinitionModel struct {
	ID             types.String `tfsdk:"id"`
	GroupTypeID    types.String `tfsdk:"group_type_id"`
	Name           types.String `tfsdk:"name"`
	Type           types.String `tfsdk:"type"`
	SortKey        types.Int64  `tfsdk:"sort_key"`
	Shorty         types.String `tfsdk:"shorty"`
	IsDefault      types.Bool   `tfsdk:"is_default"`
	IsHidden       types.Bool   `tfsdk:"is_hidden"`
	NameTranslated types.String `tfsdk:"name_translated"`
	GrowPathID     types.Int64  `tfsdk:"grow_path_id"`
}

func NewGroupRoleDefinitionResource() resource.Resource { return &groupRoleDefinitionResource{} }

var (
	_ resource.ResourceWithValidateConfig = &groupRoleDefinitionResource{}
	_ resource.ResourceWithImportState    = &groupRoleDefinitionResource{}
)

func (r *groupRoleDefinitionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_role_definition"
}

// noGroupTypeChange turns a changed group_type_id into a plan error. The
// framework would otherwise need RequiresReplace, which here means DELETE + POST.
type noGroupTypeChange struct{}

func (noGroupTypeChange) Description(context.Context) string {
	return "Refuses to change group_type_id: a role never moves to another group type."
}
func (m noGroupTypeChange) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (noGroupTypeChange) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.PlanValue.IsUnknown() || req.StateValue.IsNull() {
		return
	}
	if req.PlanValue.ValueString() != req.StateValue.ValueString() {
		resp.Diagnostics.AddAttributeError(req.Path, "Gruppentyp einer Rolle ist nicht änderbar",
			fmt.Sprintf("group_type_id soll von %s auf %s wechseln. ChurchTools verschiebt eine Rolle nicht in einen "+
				"anderen Gruppentyp; die einzige Möglichkeit wäre Löschen und Neuanlegen. Das wäre eine neue "+
				"Rollen-id: Rechte auf Typebene und in den Gruppen, Mitgliedschaften und Verweise per id gingen "+
				"verloren. Deshalb ersetzt dieser Provider Rollen nie. Stelle group_type_id auf den bisherigen Wert "+
				"zurück, oder lege bewusst eine neue Rolle an (neue Ressource) und ziehe die Mitglieder in ChurchTools um.",
				req.StateValue.ValueString(), req.PlanValue.ValueString()))
	}
}

func (r *groupRoleDefinitionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine Rolle eines Gruppentyps (z. B. Mitglied, Leiter). Ihre id ist auch die Domäne der Rechte " +
			"auf Typebene (churchtools_group_type_role). Rollen werden nie ersetzt (group_type_id ist " +
			"unveränderlich). Destroy entfernt die Rolle nur aus dem Terraform-State und löscht sie nicht in " +
			"ChurchTools (Warnung); gelöscht wird sie von Hand in der ChurchTools-Oberfläche.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The role id; also the `group_type_role` permission domain id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_type_id": schema.StringAttribute{
				Required:      true,
				Description:   "Id of the group type, e.g. churchtools_group_type.x.id. Cannot change: a change is a plan error.",
				PlanModifiers: []planmodifier.String{noGroupTypeChange{}},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Stored name of the role (2-100 characters), not the translated label.",
			},
			"type": schema.StringAttribute{
				Required:    true,
				Description: "`leader` or `participant`. No default: the classification is a statement, not a guess.",
			},
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description:   "Display order among the roles of the type. Left out: 0 on create, unmanaged afterwards.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"shorty": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "Abbreviation (1-10 characters). Left out: derived from the name on create, unmanaged afterwards.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"is_default": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description:   "Default role of its type. Left out: false on create, unmanaged afterwards.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"is_hidden": schema.BoolAttribute{
				Optional: true, Computed: true,
				Description:   "Hidden in displays that exclude hidden roles. Left out: false on create, unmanaged afterwards.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"name_translated": schema.StringAttribute{
				Computed: true,
				Description: "The name in the language of the requesting ChurchTools user. Read-only: CT does not " +
					"accept it on write, and it differs between users, so it is never compared.",
			},
			"grow_path_id": schema.Int64Attribute{
				Computed: true,
				Description: "Growth path of the role, null for none. Read-only here: never written by this " +
					"resource, but passed through unchanged on every update.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *groupRoleDefinitionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *groupRoleDefinitionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m groupRoleDefinitionModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Type.IsUnknown() && !m.Type.IsNull() && m.Type.ValueString() != "leader" && m.Type.ValueString() != "participant" {
		resp.Diagnostics.AddAttributeError(path.Root("type"), "Ungültiger Rollentyp",
			fmt.Sprintf("%q: ChurchTools kennt nur \"leader\" und \"participant\".", m.Type.ValueString()))
	}
	if !m.Name.IsUnknown() && !m.Name.IsNull() {
		if n := len([]rune(m.Name.ValueString())); n < 2 || n > 100 {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Ungültiger Rollenname",
				"Der Name einer Rolle hat 2 bis 100 Zeichen.")
		}
	}
	if !m.Shorty.IsUnknown() && !m.Shorty.IsNull() {
		if n := len([]rune(m.Shorty.ValueString())); n < 1 || n > 10 {
			resp.Diagnostics.AddAttributeError(path.Root("shorty"), "Ungültiges Kürzel", "Das Kürzel hat 1 bis 10 Zeichen.")
		}
	}
	if !m.GroupTypeID.IsUnknown() && !m.GroupTypeID.IsNull() && !digitsOnly.MatchString(m.GroupTypeID.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("group_type_id"), "Ungültige id",
			fmt.Sprintf("%q ist keine Gruppentyp-id.", m.GroupTypeID.ValueString()))
	}
}

func (m *groupRoleDefinitionModel) fromRow(row client.Row) {
	m.ID = types.StringValue(idString(row["id"]))
	m.GroupTypeID = types.StringValue(idString(row["groupTypeId"]))
	m.Name = types.StringValue(stringField(row, "name"))
	m.Type = types.StringValue(stringField(row, "type"))
	m.SortKey = types.Int64Value(intField(row, "sortKey"))
	m.Shorty = types.StringValue(stringField(row, "shorty"))
	m.IsDefault = types.BoolValue(boolField(row, "isDefault"))
	m.IsHidden = types.BoolValue(boolField(row, "isHidden"))
	m.NameTranslated = types.StringValue(stringField(row, "nameTranslated"))
	m.GrowPathID = types.Int64Null()
	if _, ok := row["growPathId"].(float64); ok {
		m.GrowPathID = types.Int64Value(intField(row, "growPathId"))
	}
}

// writeBody is the full object CT's PUT/POST wants (all eight fields are
// required): `base` supplies everything the plan leaves unknown, so values this
// configuration does not manage are carried through, not reset.
func (m groupRoleDefinitionModel) writeBody(base client.Row) (client.Row, error) {
	pick := func(v types.String, key string) any {
		if !v.IsUnknown() && !v.IsNull() {
			return v.ValueString()
		}
		return base[key]
	}
	body := client.Row{
		"groupTypeId": intField(base, "groupTypeId"),
		"name":        pick(m.Name, "name"),
		"type":        pick(m.Type, "type"),
		"shorty":      pick(m.Shorty, "shorty"),
		"sortKey":     intField(base, "sortKey"),
		"isDefault":   boolField(base, "isDefault"),
		"isHidden":    boolField(base, "isHidden"),
		"growPathId":  base["growPathId"], // never managed: null stays null
	}
	if !m.GroupTypeID.IsUnknown() && !m.GroupTypeID.IsNull() {
		n, err := strconv.ParseInt(m.GroupTypeID.ValueString(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("group_type_id %q ist keine Zahl: %w", m.GroupTypeID.ValueString(), err)
		}
		body["groupTypeId"] = n
	}
	if !m.SortKey.IsUnknown() && !m.SortKey.IsNull() {
		body["sortKey"] = m.SortKey.ValueInt64()
	}
	if !m.IsDefault.IsUnknown() && !m.IsDefault.IsNull() {
		body["isDefault"] = m.IsDefault.ValueBool()
	}
	if !m.IsHidden.IsUnknown() && !m.IsHidden.IsNull() {
		body["isHidden"] = m.IsHidden.ValueBool()
	}
	return body, nil
}

// verify compares what the read-back shows with what the plan asked for: CT can
// answer 2xx and store something else (a clamped value, an ignored field).
func (m groupRoleDefinitionModel) verify(live client.Row) error {
	var diff []string
	chk := func(name string, planned, got any, ok bool) {
		if ok && fmt.Sprint(planned) != fmt.Sprint(got) {
			diff = append(diff, fmt.Sprintf("%s: geplant %v, gespeichert %v", name, planned, got))
		}
	}
	known := func(v interface {
		IsUnknown() bool
		IsNull() bool
	}) bool {
		return !v.IsUnknown() && !v.IsNull()
	}
	chk("name", m.Name.ValueString(), stringField(live, "name"), known(m.Name))
	chk("type", m.Type.ValueString(), stringField(live, "type"), known(m.Type))
	chk("sort_key", m.SortKey.ValueInt64(), intField(live, "sortKey"), known(m.SortKey))
	chk("shorty", m.Shorty.ValueString(), stringField(live, "shorty"), known(m.Shorty))
	chk("is_default", m.IsDefault.ValueBool(), boolField(live, "isDefault"), known(m.IsDefault))
	chk("is_hidden", m.IsHidden.ValueBool(), boolField(live, "isHidden"), known(m.IsHidden))
	if len(diff) > 0 {
		return fmt.Errorf("ChurchTools hat die Rolle angenommen, aber nicht so gespeichert (%s)", strings.Join(diff, "; "))
	}
	return nil
}

func (r *groupRoleDefinitionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan groupRoleDefinitionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const summary = "Rolle konnte nicht angelegt werden"
	// Create defaults mirror ct-cli's registry: shorty from the name, a
	// participant-free sortKey 0, nothing default, nothing hidden, no growth path.
	base := client.Row{
		"shorty":     truncatePadded(plan.Name.ValueString(), 10, 1),
		"sortKey":    float64(0),
		"isDefault":  false,
		"isHidden":   false,
		"growPathId": nil,
	}
	body, err := plan.writeBody(base)
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	row, err := r.client.Create(ctx, client.GroupRolesCollection, body)
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	live, err := r.client.Get(ctx, client.GroupRolesCollection, id)
	if err != nil {
		// The role exists, only the read-back failed. Record it (id set, unknown
		// computed attributes null) so a retry does not POST a duplicate; the
		// error taints the resource.
		saved := plan
		saved.ID = types.StringValue(id)
		if saved.SortKey.IsUnknown() {
			saved.SortKey = types.Int64Null()
		}
		if saved.Shorty.IsUnknown() {
			saved.Shorty = types.StringNull()
		}
		if saved.IsDefault.IsUnknown() {
			saved.IsDefault = types.BoolNull()
		}
		if saved.IsHidden.IsUnknown() {
			saved.IsHidden = types.BoolNull()
		}
		saved.NameTranslated = types.StringNull()
		saved.GrowPathID = types.Int64Null()
		resp.Diagnostics.Append(resp.State.Set(ctx, saved)...)
		resp.Diagnostics.AddError(summary, fmt.Sprintf("Rolle %s nach dem Anlegen nicht lesbar: %s", id, err))
		return
	}
	if err := plan.verify(live); err != nil {
		// The role exists now; keep it in state so the next plan shows the drift
		// instead of creating a second one.
		var got groupRoleDefinitionModel
		got.fromRow(live)
		resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	plan.fromRow(live)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupRoleDefinitionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state groupRoleDefinitionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	live, err := r.client.Get(ctx, client.GroupRolesCollection, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Rolle konnte nicht gelesen werden", err.Error())
		return
	}
	state.fromRow(live)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *groupRoleDefinitionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state, config groupRoleDefinitionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	const summary = "Rolle konnte nicht geändert werden"
	id := state.ID.ValueString()
	// PUT is not partial. Start from the live object (not from state: the role
	// may have been edited in the UI since the last refresh) and overlay the plan.
	live, err := r.client.Get(ctx, client.GroupRolesCollection, id)
	if err != nil {
		resp.Diagnostics.AddError(summary, fmt.Sprintf("Rolle %s nicht lesbar: %s", id, err))
		return
	}
	// Optional+Computed attributes the configuration leaves out carry the old
	// STATE value in the plan (UseStateForUnknown). Sending that would revert UI
	// edits and CT side effects (a new default role clears is_default here), so
	// they are nulled and the live value wins in writeBody.
	if config.SortKey.IsNull() {
		plan.SortKey = types.Int64Null()
	}
	if config.Shorty.IsNull() {
		plan.Shorty = types.StringNull()
	}
	if config.IsDefault.IsNull() {
		plan.IsDefault = types.BoolNull()
	}
	if config.IsHidden.IsNull() {
		plan.IsHidden = types.BoolNull()
	}
	body, err := plan.writeBody(live)
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	if err := r.client.PutGroupRole(ctx, id, body); err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	after, err := r.client.Get(ctx, client.GroupRolesCollection, id)
	if err != nil {
		resp.Diagnostics.AddError(summary, fmt.Sprintf("Rolle %s nach dem Schreiben nicht lesbar: %s", id, err))
		return
	}
	if err := plan.verify(after); err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	plan.fromRow(after)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete only drops the role from Terraform state; it never calls ChurchTools.
// A role is referenced by memberships, type-level grants and rulesets by id, so
// deleting it is done by hand in the ChurchTools UI (decision eqrm/ct-structure#116).
func (r *groupRoleDefinitionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state groupRoleDefinitionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.AddWarning("Rolle nur aus dem State entfernt",
		"Rolle "+state.ID.ValueString()+" ("+state.Name.ValueString()+") wurde nur aus dem Terraform-State "+
			"entfernt und besteht in ChurchTools weiter. Gelöscht wird sie von Hand in der ChurchTools-Oberfläche.")
}

func (r *groupRoleDefinitionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digitsOnly.MatchString(req.ID) {
		resp.Diagnostics.AddError("Ungültige Import-id",
			fmt.Sprintf("%q: erwartet die Rollen-id (Zahl), z. B. 160.", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
