package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/eqrm/terraform-provider-churchtools/internal/ruleset"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.ResourceWithModifyPlan     = (*dynamicGroupResource)(nil)
	_ resource.ResourceWithValidateConfig = (*dynamicGroupResource)(nil)
	_ resource.ResourceWithImportState    = (*dynamicGroupResource)(nil)
)

// The dynamic rule of one ChurchTools group (/dynamicgroups/{id}/ruleset and
// /status): the ruleset JSON and whether it is active or switched to manual.
//
// The group itself is a churchtools_group; this resource points at it with
// group_id. It is a port of ct-cli's `dynamic` synthetic field
// (src/engine/synthetic.ts) with the same normalizer (internal/ruleset), so the
// two tools agree on what "the same ruleset" is.
//
// Writes follow ct-cli's apply: PUT the ruleset ({dynamicGroupRuleSet:[rs]}) only
// when the normalized ruleset differs from the live one, then PUT the status.
// There is deliberately NO POST /refresh: ChurchTools re-evaluates on its own.
// Rulesets are read for archived groups too (the endpoints take any group id).
type dynamicGroupResource struct {
	client *client.Client
}

type dynamicGroupModel struct {
	ID      types.String `tfsdk:"id"`
	GroupID types.String `tfsdk:"group_id"`
	Status  types.String `tfsdk:"status"`
	Ruleset RulesetValue `tfsdk:"ruleset"`
	Refs    types.Map    `tfsdk:"refs"`
}

func NewDynamicGroupResource() resource.Resource { return &dynamicGroupResource{} }

func (r *dynamicGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dynamic_group"
}

func (r *dynamicGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Die dynamische Regel einer ChurchTools Gruppe (Regelwerk und Status). Die Gruppe selbst ist " +
			"eine churchtools_group. Löschen entfernt nur den State; die Regel läuft in ChurchTools weiter.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Same as group_id. Import with `tofu import churchtools_dynamic_group.<name> <group id>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required: true,
				Description: "Id of the group (digits only), e.g. churchtools_group.x.id. Archived groups work too. " +
					"Changing it replaces the resource (the old group keeps its rule).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Required: true,
				Description: "`active` (ChurchTools keeps the membership in sync with the rule) or `manual` (the rule " +
					"is stored but not applied). There is no `none`: demoting a group to a plain one is not managed here.",
			},
			"ruleset": schema.StringAttribute{
				CustomType: RulesetType{},
				Required:   true,
				Description: "The ruleset as a JSON string, e.g. file(\"rulesets/x.json\"). The `[RuleSet]` array and the " +
					"`{dynamicGroupRuleSet:[...]}` envelope are accepted. Read-only timestamps, cosmetic `dterm` labels " +
					"and int/string differences in `query` are ignored when comparing with ChurchTools (same normalizer as " +
					"ct-cli). May contain `{\"__ctRef\":true,\"kind\":...}` markers, which `refs` resolves.",
			},
			"refs": schema.MapAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Ids for the `__ctRef` markers of the ruleset, as integers in strings. Keys: `group:<key>`, " +
					"`campus:<key>`, `group-type:<key>`, `group-type-role:<group type key>/<role>`, `person:<key>`. " +
					"Every marker needs an entry and every entry must be used by a marker. The values are normally " +
					"references to other resources (`churchtools_group.x.id`), which orders the apply.",
			},
		},
	}
}

func (r *dynamicGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

// refsKey is "<kind>:<non-empty>" with a supported kind.
var refsKeyKind = regexp.MustCompile(`^([a-z-]+):(.+)$`)

// refsValues returns the refs as a plain map. known is false while the map or
// any of its values is still unknown (a resource created in the same run).
func refsValues(m types.Map) (vals map[string]string, known bool) {
	vals = map[string]string{}
	if m.IsNull() {
		return vals, true
	}
	if m.IsUnknown() {
		return nil, false
	}
	known = true
	for k, v := range m.Elements() {
		s, ok := v.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() {
			known = false
			continue
		}
		vals[k] = s.ValueString()
	}
	return vals, known
}

func refsKeys(m types.Map) []string {
	keys := make([]string, 0, len(m.Elements()))
	for k := range m.Elements() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (r *dynamicGroupResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg dynamicGroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.GroupID.IsNull() && !cfg.GroupID.IsUnknown() && !digitsOnly.MatchString(cfg.GroupID.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("group_id"), "Ungültige Gruppen-id",
			fmt.Sprintf("%q ist keine numerische Gruppen-id.", cfg.GroupID.ValueString()))
	}
	if !cfg.Status.IsNull() && !cfg.Status.IsUnknown() {
		if s := cfg.Status.ValueString(); s != "active" && s != "manual" {
			resp.Diagnostics.AddAttributeError(path.Root("status"), "Ungültiger Status",
				fmt.Sprintf("status = %q ist nicht erlaubt. Erlaubt sind \"active\" und \"manual\"; "+
					"\"none\" (Gruppe wieder normal machen) verwaltet diese Ressource nicht.", s))
		}
	}
	if !cfg.Refs.IsNull() && !cfg.Refs.IsUnknown() {
		for _, k := range refsKeys(cfg.Refs) {
			m := refsKeyKind.FindStringSubmatch(k)
			if m == nil || !contains(ruleset.RefKeyKinds(), m[1]) {
				resp.Diagnostics.AddAttributeError(path.Root("refs"), "Ungültiger refs-Schlüssel",
					fmt.Sprintf("%q ist kein gültiger Schlüssel. Erwartet wird \"<kind>:<key>\" mit kind aus %s "+
						"(bei group-type-role: \"group-type-role:<Gruppentyp>/<Rolle>\").",
						k, strings.Join(ruleset.RefKeyKinds(), ", ")))
			}
		}
	}
	if vals, _ := refsValues(cfg.Refs); len(vals) > 0 {
		for _, k := range refsKeys(cfg.Refs) {
			if v, ok := vals[k]; ok && !ruleset.ValidID(v) {
				resp.Diagnostics.AddAttributeError(path.Root("refs"), "Ungültige id in refs",
					fmt.Sprintf("refs[%q] = %q ist keine numerische ChurchTools-id (nur Ziffern, ohne führende Nullen).", k, v))
			}
		}
	}
	if cfg.Ruleset.IsNull() || cfg.Ruleset.IsUnknown() {
		return
	}
	tree, err := ruleset.Parse(cfg.Ruleset.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ruleset"), "Regelwerk ist kein gültiges JSON", err.Error())
		return
	}
	if _, err := ruleset.Normalize(tree); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ruleset"), "Regelwerk hat die falsche Form", err.Error())
		return
	}
	markers, err := ruleset.Markers(tree)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("ruleset"), "Ungültiger __ctRef-Marker", err.Error())
		return
	}
	if cfg.Refs.IsUnknown() {
		return
	}
	// Keys are known even while their values are not, so the match is checkable now.
	have := map[string]bool{}
	for _, k := range refsKeys(cfg.Refs) {
		have[k] = true
	}
	need := map[string]bool{}
	for _, m := range markers {
		need[m.RefKey] = true
		if !have[m.RefKey] {
			resp.Diagnostics.AddAttributeError(path.Root("refs"), "Marker ohne refs-Eintrag",
				fmt.Sprintf("Das Regelwerk enthält einen __ctRef-Marker für %q, aber refs hat dafür keinen Eintrag.", m.RefKey))
		}
	}
	for _, k := range refsKeys(cfg.Refs) {
		if !need[k] {
			resp.Diagnostics.AddAttributeError(path.Root("refs"), "refs-Eintrag ohne Marker",
				fmt.Sprintf("refs[%q] wird von keinem __ctRef-Marker im Regelwerk benutzt. Eintrag entfernen "+
					"oder den Schlüssel prüfen (ein veralteter Eintrag würde sonst unbemerkt bleiben).", k))
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// resolve turns the authored ruleset into the normalized tree ChurchTools should
// hold: markers replaced by the ids in refs, then normalized.
func resolveRuleset(rs RulesetValue, refs types.Map) (map[string]any, error) {
	tree, err := ruleset.Parse(rs.ValueString())
	if err != nil {
		return nil, err
	}
	vals, known := refsValues(refs)
	if !known {
		return nil, errors.New("refs enthält noch unbekannte Werte")
	}
	resolved, err := ruleset.Resolve(tree, vals)
	if err != nil {
		return nil, err
	}
	return ruleset.Normalize(resolved)
}

// ModifyPlan keeps the state's ruleset when the configured one, with its markers
// resolved, normalizes to the same tree: ChurchTools hands back integer ids, the
// file holds markers, and without this the plan would differ on every run. While
// a ref is still unknown (its resource is created in this run) the plan keeps
// the configured value, which is right because that resource's id is new.
func (r *dynamicGroupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state dynamicGroupModel
	if resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...); resp.Diagnostics.HasError() {
		return
	}
	if resp.Diagnostics.Append(req.State.Get(ctx, &state)...); resp.Diagnostics.HasError() {
		return
	}
	if plan.Ruleset.IsNull() || plan.Ruleset.IsUnknown() || state.Ruleset.IsNull() || state.Ruleset.IsUnknown() ||
		plan.Ruleset.Equal(state.Ruleset) {
		return
	}
	want, err := resolveRuleset(plan.Ruleset, plan.Refs)
	if err != nil {
		return // ValidateConfig reports malformed input; unknown refs mean a change is coming
	}
	have, err := ruleset.NormalizeJSON(state.Ruleset.ValueString())
	if err != nil || !ruleset.Equal(want, have) {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("ruleset"), state.Ruleset)...)
}

func isStatus(err error, code int) bool {
	var se *client.StatusError
	return errors.As(err, &se) && se.Status == code
}

func rulesetPath(id string) string { return "/dynamicgroups/" + id + "/ruleset" }
func statusPath(id string) string  { return "/dynamicgroups/" + id + "/status" }

// liveRuleset reads and normalizes the group's ruleset. nil means the group has
// none (404), which is a diff, not an error.
func (r *dynamicGroupResource) liveRuleset(ctx context.Context, id string) (map[string]any, error) {
	raw, err := r.client.GetJSON(ctx, rulesetPath(id))
	if isStatus(err, 404) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ruleset.Normalize(raw)
}

// liveStatus reads the group's status; "" when it cannot be told (404).
func (r *dynamicGroupResource) liveStatus(ctx context.Context, id string) (string, error) {
	st, err := r.client.GetJSON(ctx, statusPath(id))
	if isStatus(err, 404) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if m, ok := st.(map[string]any); ok {
		if s, ok := m["dynamicGroupStatus"].(string); ok {
			return s, nil
		}
	}
	return "", nil
}

// apply writes only what differs from ChurchTools: the ruleset when the normalized
// trees differ, the status when it differs. A refs-only change therefore writes
// nothing. With create set, an existing different ruleset is an error (the group
// has to be imported first), an equal one is adopted without a write.
//
// Order: for `manual` the status goes first, so a rule that changes in the same
// apply is never evaluated live on its way to being switched off; for `active` the
// ruleset goes first, so the rule is complete before it starts running.
func (r *dynamicGroupResource) apply(ctx context.Context, plan dynamicGroupModel, create bool) error {
	id := plan.GroupID.ValueString()
	desired, err := resolveRuleset(plan.Ruleset, plan.Refs)
	if err != nil {
		return err
	}
	live, err := r.liveRuleset(ctx, id)
	if err != nil {
		return fmt.Errorf("Regelwerk von Gruppe %s konnte nicht gelesen werden: %w", id, err)
	}
	if create && live != nil && !ruleset.Equal(desired, live) {
		return fmt.Errorf("Gruppe %s hat bereits eine Regel, die vom Regelwerk in der Konfiguration abweicht. "+
			"Importiere sie zuerst (`tofu import churchtools_dynamic_group.<name> %s`), damit der Plan den Unterschied "+
			"zeigt, statt sie zu überschreiben.", id, id)
	}
	liveStatus, err := r.liveStatus(ctx, id)
	if err != nil {
		return fmt.Errorf("Status von Gruppe %s konnte nicht gelesen werden: %w", id, err)
	}
	putRuleset := func() error {
		if live != nil && ruleset.Equal(desired, live) {
			return nil
		}
		if err := r.client.Put(ctx, rulesetPath(id), ruleset.PutBody(desired)); err != nil {
			return fmt.Errorf("Regelwerk von Gruppe %s konnte nicht gespeichert werden: %w", id, err)
		}
		return nil
	}
	putStatus := func() error {
		if liveStatus == plan.Status.ValueString() {
			return nil
		}
		if err := r.client.Put(ctx, statusPath(id), map[string]any{"dynamicGroupStatus": plan.Status.ValueString()}); err != nil {
			return fmt.Errorf("Status von Gruppe %s konnte nicht gespeichert werden: %w", id, err)
		}
		return nil
	}
	steps := []func() error{putRuleset, putStatus}
	if plan.Status.ValueString() == "manual" {
		steps = []func() error{putStatus, putRuleset}
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (r *dynamicGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan dynamicGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, plan, true); err != nil {
		resp.Diagnostics.AddError("Dynamische Gruppe konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = plan.GroupID
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *dynamicGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan dynamicGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, plan, false); err != nil {
		resp.Diagnostics.AddError("Dynamische Gruppe konnte nicht aktualisiert werden", err.Error())
		return
	}
	plan.ID = plan.GroupID
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *dynamicGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state dynamicGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.GroupID.ValueString()

	raw, err := r.client.GetJSON(ctx, rulesetPath(id))
	switch {
	case isStatus(err, 404):
		// No ruleset: either the group does not exist or it is a plain group.
		// Only the first removes the resource; the second plans a diff.
		if _, gerr := r.client.Get(ctx, groupCollection, id); errors.Is(gerr, client.ErrNotFound) {
			resp.State.RemoveResource(ctx)
			return
		} else if gerr != nil {
			resp.Diagnostics.AddError("Gruppe konnte nicht gelesen werden", gerr.Error())
			return
		}
		state.ID = state.GroupID
		state.Status = types.StringValue("none")
		state.Ruleset = RulesetValue{StringValue: types.StringValue("{}")}
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	case err != nil:
		// Anything else, 403 and 429 included, must not look like "no ruleset":
		// that would plan a PUT over a rule we merely could not read.
		resp.Diagnostics.AddError("Regelwerk konnte nicht gelesen werden", err.Error())
		return
	}
	live, err := ruleset.Normalize(raw)
	if err != nil {
		resp.Diagnostics.AddError("Regelwerk hat eine unerwartete Form", err.Error())
		return
	}
	st, err := r.client.GetJSON(ctx, statusPath(id))
	if err != nil {
		resp.Diagnostics.AddError("Status der dynamischen Gruppe konnte nicht gelesen werden", err.Error())
		return
	}
	status := "none"
	if m, ok := st.(map[string]any); ok {
		if s, ok := m["dynamicGroupStatus"].(string); ok && s != "" {
			status = s
		}
	}

	// Keep the authored text (markers, formatting) while it still means what
	// ChurchTools holds; otherwise show the live ruleset so the diff is real.
	keep := false
	if !state.Ruleset.IsNull() && !state.Ruleset.IsUnknown() {
		if want, rerr := resolveRuleset(state.Ruleset, state.Refs); rerr == nil && ruleset.Equal(want, live) {
			keep = true
		}
	}
	if !keep {
		text, cerr := ruleset.Canonical(live)
		if cerr != nil {
			resp.Diagnostics.AddError("Regelwerk konnte nicht serialisiert werden", cerr.Error())
			return
		}
		state.Ruleset = RulesetValue{StringValue: types.StringValue(text)}
	}
	state.ID = state.GroupID
	state.Status = types.StringValue(status)
	if state.Refs.IsUnknown() {
		state.Refs = types.MapNull(types.StringType)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete un-manages. The rule keeps running in ChurchTools; see
// dynamicGroupOnDeleteDetail.
func (r *dynamicGroupResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, dynamicGroupOnDeleteDetail)
}

func (r *dynamicGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digitsOnly.MatchString(req.ID) {
		resp.Diagnostics.AddError("Ungültige Gruppen-id",
			fmt.Sprintf("%q ist keine numerische Gruppen-id. Nutze `tofu import churchtools_dynamic_group.<name> <id>` "+
				"mit der Gruppen-id aus der ChurchTools-URL (/groups/<id>).", req.ID))
		return
	}
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	// Import adopts an existing rule. A group without one (or an id that does not
	// exist) is refused, so a wrong id in a host map cannot promote a plain group.
	// Promoting is still possible by creating the resource without an import.
	if _, err := r.client.GetJSON(ctx, rulesetPath(req.ID)); isStatus(err, 404) {
		resp.Diagnostics.AddError("Gruppe hat keine dynamische Regel",
			fmt.Sprintf("Gruppe %s hat in ChurchTools kein Regelwerk (oder es gibt die Gruppe nicht). Import übernimmt nur "+
				"bestehende Regeln. Prüfe die Gruppen-id; soll die Gruppe erst dynamisch werden, lege die Ressource "+
				"ohne import-Block an.", req.ID))
		return
	} else if err != nil {
		resp.Diagnostics.AddError("Regelwerk konnte nicht gelesen werden", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), req.ID)...)
	// refs stays null; ruleset and status come from Read.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("refs"), types.MapNull(types.StringType))...)
}
