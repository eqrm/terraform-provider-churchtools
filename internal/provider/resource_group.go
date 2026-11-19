package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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
// visibility ("Sichtbarkeit") is Optional + Computed like inform_leader: left out, the
// provider does not manage it (no diff, never sent, CT's value is read into state);
// set, it is enforced and drift-detected. Read: `settings.visibility`. Write: a flat
// top-level `visibility`, which BOTH POST and PATCH take (measured on eqrm-dev,
// 2026-11-06: a create with "intern" stood on intern; a PATCH answered 200 and moved it).
// Values: hidden, intern, restricted, public; a new group defaults to restricted. CT
// does NOT validate the value (a PATCH with "bogus" answered 200 and stored it), so the
// schema does. `hidden` matters: type rights (permission 1108) do not apply to hidden groups.
//
// subgroup_permission_depth ("Wie viele Ebenen von Untergruppen sollen einbezogen werden?")
// is Optional + Computed too, but costs a ~4 MB read, so it is stricter about unset: never
// set, nothing is read, written or diffed and state holds null (removed from config after
// being set, it is no longer written, but state keeps the value and Read still refreshes it). An import leaves it null
// too (Read never sees config); the first plan after importing with the attribute set shows
// null -> N, and that apply writes N (idempotent) and reads it back. It is NOT in REST: it lives on the
// legacy churchauth ajax (client/groupdepth.go). CT validates neither the value nor the
// group id on write, so the schema validates the value and every write is read back.
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
	Visibility    types.String `tfsdk:"visibility"`

	SubgroupPermissionDepth types.Int64 `tfsdk:"subgroup_permission_depth"`
}

// depthManaged reports whether subgroup_permission_depth is set in config (see informLeaderManaged).
func depthManaged(v types.Int64) bool { return !v.IsNull() && !v.IsUnknown() }

type oneOfInt64 []int64

func (v oneOfInt64) Description(context.Context) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return "value must be one of: " + strings.Join(parts, ", ")
}
func (v oneOfInt64) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v oneOfInt64) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, ok := range v {
		if req.ConfigValue.ValueInt64() == ok {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Ungültiger Wert",
		fmt.Sprintf("%d ist nicht erlaubt. %s (999 = alle Ebenen).", req.ConfigValue.ValueInt64(), v.Description(ctx)))
}

// writeDepth sets the depth, reads it back and errors if CT did not store it. The
// returned value is what CT holds (null when that could not be read).
func (r *groupResource) writeDepth(ctx context.Context, id string, want int64) (types.Int64, error) {
	if err := r.client.SetGroupPermissionDepth(ctx, id, want); err != nil {
		return types.Int64Null(), fmt.Errorf("subgroup_permission_depth = %d konnte nicht gesetzt werden: %w", want, err)
	}
	got, err := r.client.GroupPermissionDepth(ctx, id)
	if err != nil {
		return types.Int64Null(), fmt.Errorf("subgroup_permission_depth = %d wurde gesendet, konnte aber nicht zurückgelesen werden: %w", want, err)
	}
	if got != want {
		return types.Int64Value(got), fmt.Errorf("Die Änderung an subgroup_permission_depth mit %d wurde angenommen "+
			"(ChurchTools antwortet auch bei unbekannten Gruppen mit Erfolg), die Gruppe %s steht danach aber auf %d. "+
			"Prüfe, ob die Gruppe existiert und der Token ihre Berechtigungen ändern darf", want, id, got)
	}
	return types.Int64Value(got), nil
}

// groupVisibilities are the values ChurchTools knows for a group's visibility.
var groupVisibilities = []string{"hidden", "intern", "restricted", "public"}

// groupVisibilityKey is the request key of visibility; the row serves it as `settings.visibility`.
const groupVisibilityKey = "visibility"

// visibilityManaged reports whether visibility is set in config (see informLeaderManaged).
func visibilityManaged(v types.String) bool { return !v.IsNull() && !v.IsUnknown() }

type oneOfString []string

func (v oneOfString) Description(context.Context) string {
	return "value must be one of: " + strings.Join(v, ", ")
}
func (v oneOfString) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v oneOfString) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, ok := range v {
		if req.ConfigValue.ValueString() == ok {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Ungültiger Wert",
		fmt.Sprintf("%q ist nicht erlaubt. Erlaubt: %s.", req.ConfigValue.ValueString(), strings.Join(v, ", ")))
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
			"visibility": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "Sichtbarkeit der Gruppe: one of hidden, intern, restricted, public. Note that " +
					"type rights (permission 1108) do not apply to hidden groups. Left out, the provider " +
					"does not manage it: no diff, nothing sent, the value ChurchTools holds (restricted for " +
					"a new group) is read into state. Set, it is enforced and drift is reported.",
				Validators:    []validator.String{oneOfString(groupVisibilities)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"subgroup_permission_depth": schema.Int64Attribute{
				Optional: true,
				Computed: true,
				Description: "Berechtigungen -> \"Wie viele Ebenen von Untergruppen sollen einbezogen werden?\": " +
					"how many levels of subgroups the group's role permissions reach (0 = none, 1, 2, 3, 999 = all " +
					"levels). Not the group type's permission_depth. Never set, the provider does not manage it: " +
					"nothing is read or written (the read is ~4 MB), the state holds null. Set, it is enforced and " +
					"drift is reported. Removed from config later, it is no longer written, but the last value stays " +
					"in state and is still refreshed. ChurchTools does not validate the value, so only 0, 1, 2, 3 and 999 are accepted.",
				Validators:    []validator.Int64{oneOfInt64(client.SubgroupDepths)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
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

	// The depth is written after the group exists; until then state holds null.
	wantDepth := plan.SubgroupPermissionDepth
	plan.SubgroupPermissionDepth = types.Int64Null()

	body := client.Row{}
	for _, f := range groupFields {
		v, err := groupValue(f.key, f.attr, f.get(plan))
		if err != nil {
			resp.Diagnostics.AddError("Gruppe konnte nicht angelegt werden", err.Error())
			return
		}
		body[f.key] = v // campusId stays an explicit null when unset
	}
	if visibilityManaged(plan.Visibility) {
		body[groupVisibilityKey] = plan.Visibility.ValueString() // POST takes it (eqrm-dev, 2026-11-06)
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
		if plan.Visibility.IsUnknown() {
			plan.Visibility = types.StringNull()
		}
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
	haveRow := groupFromRow(id, got)
	have := haveRow.InformLeader
	// State holds what CT stored; a visibility CT ignored is reported after inform_leader
	// is settled, so the error leaves only the field CT refused.
	wantVis := plan.Visibility
	plan.Visibility = haveRow.Visibility
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
	if depthManaged(wantDepth) {
		got, err := r.writeDepth(ctx, id, wantDepth.ValueInt64())
		plan.SubgroupPermissionDepth = got
		if err != nil {
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
			resp.Diagnostics.AddError("Gruppe wurde angelegt, subgroup_permission_depth aber nicht gesetzt", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	if visibilityManaged(wantVis) && !wantVis.Equal(plan.Visibility) {
		resp.Diagnostics.AddError("ChurchTools hat die Änderung ignoriert",
			fmt.Sprintf("Die Gruppe %s/%s wurde mit visibility = %s angelegt, steht danach aber auf %s.",
				groupCollection, id, groupDisplay(wantVis), groupDisplay(plan.Visibility)))
	}
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
		Visibility:    nestedString(row, "settings", groupVisibilityKey),
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
	m := groupFromRow(state.ID.ValueString(), row)
	m.SubgroupPermissionDepth = state.SubgroupPermissionDepth
	// Only a group that holds a depth in state (set in config, or imported) pays for the
	// ~4 MB read; the client caches it for the rest of the run.
	if !state.SubgroupPermissionDepth.IsNull() {
		n, err := r.client.GroupPermissionDepth(ctx, state.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("subgroup_permission_depth konnte nicht gelesen werden", err.Error())
			return
		}
		m.SubgroupPermissionDepth = types.Int64Value(n)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
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
	var cfgVisibility types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("visibility"), &cfgVisibility)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var cfgDepth types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("subgroup_permission_depth"), &cfgDepth)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	// The depth goes first: it is a separate endpoint, and from here `depth` is what
	// every later state write must carry. Unset in config, state's value (null or not)
	// stays; the plan can't tell, as it is unknown for a null state.
	depth := state.SubgroupPermissionDepth
	if depthManaged(cfgDepth) && !cfgDepth.Equal(state.SubgroupPermissionDepth) {
		got, err := r.writeDepth(ctx, id, cfgDepth.ValueInt64())
		if err != nil {
			if !got.IsNull() {
				// CT holds something else than asked: record it so the next plan shows the gap.
				resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("subgroup_permission_depth"), got)...)
			}
			resp.Diagnostics.AddError("subgroup_permission_depth konnte nicht gesetzt werden", err.Error())
			return
		}
		depth = got
	}
	plan.SubgroupPermissionDepth = depth

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
	if visibilityManaged(cfgVisibility) && !plan.Visibility.Equal(state.Visibility) {
		body[groupVisibilityKey] = plan.Visibility.ValueString()
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
	migrated.SubgroupPermissionDepth = depth
	if len(body) > 0 {
		if _, err := r.client.Update(ctx, groupCollection, id, "PATCH", body); err != nil {
			resp.Diagnostics.AddError("Gruppe konnte nicht aktualisiert werden", err.Error())
			if typeChanged {
				// The migration already landed; record it so the next plan starts from the truth.
				if after, gerr := r.client.Get(ctx, groupCollection, id); gerr == nil {
					m := groupFromRow(id, after)
					m.SubgroupPermissionDepth = depth
					resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
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
	got.SubgroupPermissionDepth = depth
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
	if visibilityManaged(cfgVisibility) && !plan.Visibility.Equal(got.Visibility) {
		resp.Diagnostics.AddError(
			"ChurchTools hat die Änderung ignoriert",
			fmt.Sprintf("Die Änderung an %s/%s mit visibility = %s wurde angenommen, die Gruppe steht danach "+
				"aber auf %s. Prüfe in ChurchTools, ob dieses Feld für die Gruppe gesperrt ist, und passe die "+
				"Konfiguration an den Wert an, der dort gilt.",
				groupCollection, id, groupDisplay(plan.Visibility), groupDisplay(got.Visibility)))
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
