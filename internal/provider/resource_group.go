package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithModifyPlan = (*groupResource)(nil)

const groupCollection = "/groups"

// A ChurchTools group (Gruppe, /groups) — the group itself, nothing around it.
//
// Five settings: name, group type, group status, campus and inform_leader. Hierarchy, dynamic
// rulesets, member fields and grants are deliberately NOT here; they will be
// separate resources that point at this one (`churchtools_group_parent`,
// `churchtools_dynamic_group`, and
// `churchtools_group_member_field { group_id = churchtools_group.x.id }`).
//
// Foreign ids are strings so `campus_id = churchtools_campus.x.id` works and the
// number literals ct-cli's exporter writes convert without ceremony. The wire
// wants ints, so they are parsed on the way out.
//
// campus_id is Optional and NOT Computed: null means "no campus" and is enforced
// (ct-cli sends `campusId ?? null`, and `ct export tf` omits null fields). "0" is
// a real campus and must survive as "0".
//
// inform_leader ("Leiter bei Änderungen informieren") is Optional + Computed: left
// out of config the provider does not manage it (no diff, never sent, the value
// CT holds is read into state). Set, it is enforced and drift-detected. CT mails
// the leaders of the group AND of its parent groups on every membership change
// while it is on. Read: `settings.informLeader` (bool). Write: a flat top-level
// `informLeader`, which PATCH takes and POST ignores (measured on eqrm-dev,
// 2026-10-26), so Create sets it with a PATCH after the POST. Every write is read
// back, so a key CT ignores is a loud error, not silent drift.
//
// Read shape, probed read-only on eqrm-dev on 2026-10-16: the row nests the three
// ids under `information`, and campusId is always there, JSON null or an int.
// Writes take them FLAT at the top level. POST and PATCH ran live on eqrm-dev
// on 2026-10-16 (PATCH answers 200, campusId null clears); Update still never
// trusts the reply body and reads the group back instead.
type groupResource struct {
	client *client.Client
}

type groupModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	GroupTypeID   types.String `tfsdk:"group_type_id"`
	GroupStatusID types.String `tfsdk:"group_status_id"`
	CampusID      types.String `tfsdk:"campus_id"`
	InformLeader  types.Bool   `tfsdk:"inform_leader"`
}

func NewGroupResource() resource.Resource { return &groupResource{} }

func (r *groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}

func (r *groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine ChurchTools Gruppe (Name, Gruppentyp, Status, Standort). Hierarchie, " +
			"dynamische Gruppen, Mitglieder-Datenfelder und Berechtigungen gehören nicht dazu; dafür " +
			"kommen eigene Ressourcen (churchtools_group_parent, churchtools_dynamic_group, " +
			"churchtools_group_member_field mit group_id = churchtools_group.x.id).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "ChurchTools group id (numeric). Import with `tofu import churchtools_group.<name> <id>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true, Description: "Group name."},
			"group_type_id": schema.StringAttribute{
				Required: true,
				Description: "Id of the group type, e.g. churchtools_group_type.x.id. Changing it migrates the " +
					"group (POST /groups/{id}/grouptype), because ChurchTools refuses the field on a plain update. " +
					"Every role of the old type is mapped to the same-named role of the new one; a role without " +
					"members and without a same-named target goes to the new type's default role. A role that " +
					"holds members and has no same-named target stops plan and apply with an error, since the " +
					"mapping decides where those memberships land.",
			},
			"group_status_id": schema.StringAttribute{
				Required:    true,
				Description: "Id of the group status.",
			},
			"campus_id": schema.StringAttribute{
				Optional: true,
				Description: "Id of the campus, e.g. churchtools_campus.x.id. \"0\" is a valid campus. " +
					"Left out, the group has no campus — and a campus set in ChurchTools is removed.",
			},
			"inform_leader": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "\"Leiter bei Änderungen informieren\": when true, ChurchTools emails the leaders of " +
					"the group and of its parent groups on every membership change. Left out, the provider does " +
					"not manage it: no diff, nothing sent, the value ChurchTools holds is read into state. " +
					"Set, it is enforced and drift is reported.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

// groupWireID parses a foreign id for the request body. null is returned as nil
// so a cleared campus goes over the wire as an explicit JSON null.
func groupWireID(attr string, v types.String) (any, error) {
	if v.IsNull() || v.IsUnknown() {
		return nil, nil
	}
	n, err := strconv.ParseInt(v.ValueString(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s = %q ist keine numerische ChurchTools-id", attr, v.ValueString())
	}
	return n, nil
}

// groupFields lists the managed settings once: request key, attribute name, and
// how to read each from a model. Create, Update and the read-back all walk it.
var groupFields = []struct {
	key, attr string
	get       func(groupModel) types.String
}{
	{"name", "name", func(m groupModel) types.String { return m.Name }},
	{"groupTypeId", "group_type_id", func(m groupModel) types.String { return m.GroupTypeID }},
	{"groupStatusId", "group_status_id", func(m groupModel) types.String { return m.GroupStatusID }},
	{"campusId", "campus_id", func(m groupModel) types.String { return m.CampusID }},
}

// groupInformLeaderKey is the request key of inform_leader; the row serves it as
// `settings.informLeader`.
const groupInformLeaderKey = "informLeader"

// informLeaderManaged reports whether a value of inform_leader is set. Pass the
// config value: with UseStateForUnknown an unset attribute plans as unknown on
// create but as the state value on update, so the plan can't tell in Update.
func informLeaderManaged(v types.Bool) bool { return !v.IsNull() && !v.IsUnknown() }

// groupValue turns one managed setting into its request value.
func groupValue(key, attr string, v types.String) (any, error) {
	if key == "name" {
		return v.ValueString(), nil
	}
	return groupWireID(attr, v)
}

func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := client.Row{}
	for _, f := range groupFields {
		v, err := groupValue(f.key, f.attr, f.get(plan))
		if err != nil {
			resp.Diagnostics.AddError("Gruppe konnte nicht angelegt werden", err.Error())
			return
		}
		body[f.key] = v // campusId stays an explicit null when unset
	}

	row, err := r.client.Create(ctx, groupCollection, body)
	if err != nil {
		// CT 400s a POST whose name an existing group already has ("Duplicate
		// found. Use force flag …", messageKey forbidden.duplicate.group, seen
		// live in ct-cli#75). This resource never sends `force`, so a same-named
		// group is almost always one to import, not to create.
		resp.Diagnostics.AddError("Gruppe konnte nicht angelegt werden",
			err.Error()+"\n\nExistiert die Gruppe bereits in ChurchTools, importiere sie statt sie "+
				"neu anzulegen: `tofu import churchtools_group.<name> <id>`.")
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Gruppe konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	// POST ignores informLeader (live on eqrm-dev, 2026-10-26: a create with
	// `informLeader: true` answered 200 and the group stood on false), while PATCH
	// takes it. So the setting is never sent on create: read what CT gave the new
	// group, then PATCH it if config wants something else.
	got, gerr := r.client.Get(ctx, groupCollection, id)
	if gerr != nil {
		want := plan.InformLeader
		plan.InformLeader = types.BoolNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		if want.IsUnknown() {
			// Only the computed value is missing; the next refresh fills it. An error
			// here would taint a group that was created correctly.
			resp.Diagnostics.AddWarning("inform_leader der neuen Gruppe konnte nicht gelesen werden", gerr.Error())
		} else {
			resp.Diagnostics.AddError("Gruppe wurde angelegt, aber nicht zurückgelesen", gerr.Error())
		}
		return
	}
	have := groupFromRow(id, got).InformLeader
	if informLeaderManaged(plan.InformLeader) && !plan.InformLeader.Equal(have) {
		if _, err := r.client.Update(ctx, groupCollection, id, "PATCH",
			client.Row{groupInformLeaderKey: plan.InformLeader.ValueBool()}); err != nil {
			want := plan.InformLeader
			plan.InformLeader = have
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
			resp.Diagnostics.AddError("Gruppe wurde angelegt, inform_leader aber nicht gesetzt",
				fmt.Sprintf("inform_leader = %t: %s", want.ValueBool(), err.Error()))
			return
		}
		after, err := r.client.Get(ctx, groupCollection, id)
		if err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
			resp.Diagnostics.AddError("Gruppe konnte nach dem Speichern nicht gelesen werden", err.Error())
			return
		}
		have = groupFromRow(id, after).InformLeader
		if !plan.InformLeader.Equal(have) {
			want := plan.InformLeader
			plan.InformLeader = have
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
			resp.Diagnostics.AddError("ChurchTools hat die Änderung ignoriert",
				fmt.Sprintf("Die Änderung an %s/%s mit inform_leader = %t wurde angenommen, die Gruppe steht "+
					"danach aber auf %s.", groupCollection, id, want.ValueBool(), have))
			return
		}
	}
	plan.InformLeader = have
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// groupFromRow maps a GET /groups/{id} row onto the model.
func groupFromRow(id string, row client.Row) groupModel {
	return groupModel{
		ID:            types.StringValue(id),
		Name:          types.StringValue(stringField(row, "name")),
		GroupTypeID:   nestedIDString(row, "information", "groupTypeId"),
		GroupStatusID: nestedIDString(row, "information", "groupStatusId"),
		CampusID:      nestedIDString(row, "information", "campusId"),
		InformLeader:  nestedBool(row, "settings", groupInformLeaderKey),
	}
}

func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	row, err := r.client.Get(ctx, groupCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	// Anything else, 403 included, is an error: dropping state because the token
	// may not see a group would plan a duplicate.
	if err != nil {
		resp.Diagnostics.AddError("Gruppe konnte nicht gelesen werden", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, groupFromRow(state.ID.ValueString(), row))...)
}

func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var cfgInformLeader types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("inform_leader"), &cfgInformLeader)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	// PATCH only what changed: it leaves everything else on the group (and
	// anything edited in the UI since the last refresh) alone.
	body := client.Row{}
	typeChanged := false
	for _, f := range groupFields {
		if f.get(plan).Equal(f.get(state)) {
			continue
		}
		if f.key == "groupTypeId" {
			// CT 400s groupTypeId on PATCH; the type moves through its own endpoint below.
			typeChanged = true
			continue
		}
		v, err := groupValue(f.key, f.attr, f.get(plan))
		if err != nil {
			resp.Diagnostics.AddError("Gruppe konnte nicht aktualisiert werden", err.Error())
			return
		}
		body[f.key] = v
	}
	if informLeaderManaged(cfgInformLeader) && !plan.InformLeader.Equal(state.InformLeader) {
		body[groupInformLeaderKey] = plan.InformLeader.ValueBool()
	}
	// The migration goes first and is fully planned (roles, members, mapping) before
	// any write, so a refusal leaves the group untouched.
	if typeChanged {
		mig, err := r.planTypeChange(ctx, id, state.GroupTypeID, plan.GroupTypeID)
		if err != nil {
			resp.Diagnostics.AddError("Gruppentyp kann nicht gewechselt werden", err.Error())
			return
		}
		if err := r.client.Post(ctx, groupCollection+"/"+id+"/grouptype", mig.body()); err != nil {
			resp.Diagnostics.AddError("Gruppentyp konnte nicht gewechselt werden", err.Error())
			return
		}
	}
	// Once the migration POST succeeded, the group's type is the planned one whatever
	// happens next; every later error path must record that, not the stale state.
	migrated := state
	migrated.GroupTypeID = plan.GroupTypeID
	if len(body) > 0 {
		if _, err := r.client.Update(ctx, groupCollection, id, "PATCH", body); err != nil {
			resp.Diagnostics.AddError("Gruppe konnte nicht aktualisiert werden", err.Error())
			if typeChanged {
				// The migration already landed; record it so the next plan starts from the truth.
				if after, gerr := r.client.Get(ctx, groupCollection, id); gerr == nil {
					resp.Diagnostics.Append(resp.State.Set(ctx, groupFromRow(id, after))...)
				}
			}
			return
		}
	}

	// Don't trust the PATCH reply (200 live, but its body is not the contract): read the
	// group back. This also catches CT accepting a key and ignoring it (the same
	// failure db_field hit on locked built-ins), which would otherwise be stored
	// as state and re-planned on every run.
	after, err := r.client.Get(ctx, groupCollection, id)
	if err != nil {
		resp.Diagnostics.AddError("Gruppe konnte nach dem Speichern nicht gelesen werden", err.Error())
		if typeChanged {
			resp.Diagnostics.Append(resp.State.Set(ctx, migrated)...)
		}
		return
	}
	got := groupFromRow(id, after)
	for _, f := range groupFields {
		if want := f.get(plan); !want.Equal(f.get(got)) {
			resp.Diagnostics.AddError(
				"ChurchTools hat die Änderung ignoriert",
				fmt.Sprintf("Die Änderung an %s/%s mit %s = %s wurde angenommen, die Gruppe steht danach aber auf "+
					"%s. Prüfe in ChurchTools, ob dieses Feld für die Gruppe gesperrt ist, und passe die "+
					"Konfiguration an den Wert an, der dort gilt.",
					groupCollection, id, f.attr, groupDisplay(want), groupDisplay(f.get(got))))
		}
	}
	if informLeaderManaged(cfgInformLeader) && !plan.InformLeader.Equal(got.InformLeader) {
		resp.Diagnostics.AddError(
			"ChurchTools hat die Änderung ignoriert",
			fmt.Sprintf("Die Änderung an %s/%s mit inform_leader = %t wurde angenommen, die Gruppe steht danach "+
				"aber auf %s. Prüfe in ChurchTools, ob dieses Feld für die Gruppe gesperrt ist, und passe die "+
				"Konfiguration an den Wert an, der dort gilt.",
				groupCollection, id, plan.InformLeader.ValueBool(), got.InformLeader))
	}
	if resp.Diagnostics.HasError() {
		if typeChanged {
			// The migration landed; keep the state on what CT holds, so the next plan
			// shows only the field CT ignored.
			resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
		}
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, got)...)
}

// planTypeChange resolves the role mapping for a group_type_id change.
func (r *groupResource) planTypeChange(ctx context.Context, id string, from, to types.String) (groupTypeMigration, error) {
	fromID, err := groupWireID("group_type_id", from)
	if err != nil {
		return groupTypeMigration{}, err
	}
	toID, err := groupWireID("group_type_id", to)
	if err != nil {
		return groupTypeMigration{}, err
	}
	f, ok := fromID.(int64)
	if !ok {
		return groupTypeMigration{}, fmt.Errorf("Der bisherige group_type_id der Gruppe %s ist nicht gesetzt (%s), "+
			"etwa nach einem Import ohne Gruppentyp. Ohne ihn lässt sich kein Rollen-Mapping ableiten.", id, groupDisplay(from))
	}
	t, ok := toID.(int64)
	if !ok {
		return groupTypeMigration{}, fmt.Errorf("Der neue group_type_id der Gruppe %s ist nicht gesetzt (%s). "+
			"Ohne ihn lässt sich kein Rollen-Mapping ableiten.", id, groupDisplay(to))
	}
	return planGroupTypeMigration(ctx, r.client, id, f, t)
}

// ModifyPlan refuses a group type change that cannot apply (a role with members and
// no same-named role in the target type) already at plan time, so the plan does not
// look applicable. It stays quiet while the provider is unconfigured or either type
// id is still unknown (a group type created in the same run); Update repeats the
// check before its first write.
func (r *groupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.client == nil || req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state groupModel
	if resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...); resp.Diagnostics.HasError() {
		return
	}
	if resp.Diagnostics.Append(req.State.Get(ctx, &state)...); resp.Diagnostics.HasError() {
		return
	}
	if plan.ID.IsUnknown() || state.ID.IsNull() ||
		plan.GroupTypeID.IsUnknown() || plan.GroupTypeID.IsNull() || state.GroupTypeID.IsNull() ||
		plan.GroupTypeID.Equal(state.GroupTypeID) {
		return
	}
	mig, err := r.planTypeChange(ctx, state.ID.ValueString(), state.GroupTypeID, plan.GroupTypeID)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("group_type_id"), "Gruppentyp kann nicht gewechselt werden", err.Error())
		return
	}
	resp.Diagnostics.AddAttributeWarning(path.Root("group_type_id"), "Rollen-Mapping beim Gruppentyp-Wechsel",
		"Beim Apply sendet der Provider dieses Mapping (alte Rolle -> neue Rolle):\n"+mig.describe())
}

func groupDisplay(v types.String) string {
	if v.IsNull() {
		return "null (kein Wert)"
	}
	return strconv.Quote(v.ValueString())
}

// Delete un-manages without deleting. CT's DELETE on a group answers 409 while
// anything references it and destroys its memberships otherwise, so it is never
// called — see orphanOnDeleteDetail.
func (r *groupResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Gruppen"))
}

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

func (r *groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// A name or a typo would otherwise be sent to GET /groups/<text> and surface
	// as a confusing 404 or 400.
	if !digitsOnly.MatchString(req.ID) {
		resp.Diagnostics.AddError("Ungültige Gruppen-id",
			fmt.Sprintf("%q ist keine numerische Gruppen-id. Nutze `tofu import churchtools_group.<name> <id>` "+
				"mit der Zahl aus der ChurchTools-URL (/groups/<id>).", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
