package provider

// The framework layer shared by every resource that owns one permission domain:
// churchtools_group_role (group_role), churchtools_group_type_role
// (group_type_role) and churchtools_person_status_grants (status).
//
// The engine itself (diffing, preserve_unknown, filters, exact revokes) lives in
// internal/grants and knows no Terraform types. This file holds what is left:
// the `grant` block schema, plan-time validation, the conversion between the
// block set and engine rows, and the create/read/update flow around one domain
// id. A resource supplies its domain type, how it resolves the domain id, and
// the wording of its messages.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/eqrm/terraform-provider-churchtools/internal/grants"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// grantsKind describes one permission domain as far as the shared layer cares.
type grantsKind struct {
	// domain is the CT domainType: group_role, group_type_role or status.
	domain string
	// subgroups: the grant block offers applies_to_subgroups (isInherited rows).
	// CT allows it on the role domains only.
	subgroups bool
	// authIDBelow, when > 0, is CT's upper bound for a right's authId on this
	// domain (status: < 10000). Checked at plan time.
	authIDBelow int64
}

var (
	groupRoleKind     = grantsKind{domain: "group_role", subgroups: true}
	groupTypeRoleKind = grantsKind{domain: "group_type_role", subgroups: true}
	statusKind        = grantsKind{domain: "status", authIDBelow: 10000}
)

// grantsData is the part of a resource model every grants resource has.
type grantsData struct {
	Grants    types.Set
	Preserve  types.Set
	Preserved types.Set
}

func grantAttrTypesFor(subgroups bool) map[string]attr.Type {
	m := map[string]attr.Type{"right": types.StringType}
	if subgroups {
		m["applies_to_subgroups"] = types.BoolType
	}
	for _, a := range grants.ScopeAttrs {
		m[a] = types.SetType{ElemType: types.StringType}
	}
	return m
}

func grantObjectTypeFor(subgroups bool) types.ObjectType {
	return types.ObjectType{AttrTypes: grantAttrTypesFor(subgroups)}
}

// grantsAttributes are the preserve_unknown / preserved_grants arguments.
func grantsAttributes(what string) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"preserve_unknown": schema.SetAttribute{
			ElementType: types.StringType,
			Optional:    true,
			Description: "Scope dimensions (CT scope fields such as `crp_query`, `cc_html_template`, " +
				"`cdb_gruppe`) on which grants NOT declared here are left alone instead of revoked. " +
				"Unscoped rights are never preserved. Left out, every undeclared grant is revoked.",
		},
		"preserved_grants": schema.SetAttribute{
			ElementType: types.StringType,
			Computed:    true,
			Description: "The live grants preserve_unknown keeps out of this resource, for review: " +
				"„I forgot one“ and „I deliberately left the module grants alone“ must never look alike.",
			PlanModifiers: []planmodifier.Set{preservedGrantsModifier{}},
		},
	}
}

func grantBlock(kind grantsKind) schema.SetNestedBlock {
	idSet := func(desc string) schema.SetAttribute {
		return schema.SetAttribute{ElementType: types.StringType, Optional: true, Description: desc}
	}
	attrs := map[string]schema.Attribute{
		"right": schema.StringAttribute{
			Required:    true,
			Description: "Right as `module:name` from CT's permission catalog, e.g. \"churchdb:view alldata\".",
		},
		"groups":          idSet("Group ids (scope cdb_gruppe)."),
		"campuses":        idSet("Campus ids (scope cdb_station). -1 = all."),
		"departments":     idSet("Department ids (scope cdb_bereich). -1 = all."),
		"security_levels": idSet("Security levels (scope cc_securitylevel)."),
		"group_types":     idSet("Group type ids (scope cdb_gruppentyp)."),
		"comment_viewers": idSet("Comment viewer ids (scope cdb_comment_viewer)."),
		grants.RawDataIDsAttr: idSet("Raw data ids, for every other scope dimension " +
			"(crp_query, cc_html_template, cc_calcategory, …). For oauth_client scopes, -1 = all."),
	}
	if kind.subgroups {
		attrs["applies_to_subgroups"] = schema.BoolAttribute{
			Optional: true,
			Description: "true: the row CT marks isInherited, which applies in the SUBGROUPS of " +
				"the group (for a type-level role: in the subgroups of every group of the type), not in " +
				"the group itself. Left out: the group itself. Never write false.",
		}
	}
	return schema.SetNestedBlock{
		Description: "One right. Without a scope argument it is the right's row without a data " +
			"selection; with one, one row per id. A row ChurchTools itself owns (system baseline, " +
			"`meta.modifiedPid` -1) counts as satisfying a declared grant with the same right, selection and " +
			"`applies_to_subgroups`: no write, no diff. Undeclared system rows are ignored. The provider never " +
			"revokes a system row; dropping its block only takes it out of state (with a warning). After an " +
			"import, which leaves undeclared system rows out of state, a block declaring one shows once as an " +
			"addition and is adopted without a write.",
		NestedObject: schema.NestedBlockObject{Attributes: attrs},
	}
}

var dataIDPattern = regexp.MustCompile(`^(-1|0|[1-9][0-9]*)$`)

func strAttr(attrs map[string]attr.Value, key string) (types.String, bool) {
	v, ok := attrs[key].(types.String)
	return v, ok
}

// validateGrants catches at plan time what would otherwise be a silent wrong
// grant: a scope on the wrong dimension, an explicit false that Read can never
// reproduce, two blocks Read would merge into one.
func validateGrants(ctx context.Context, kind grantsKind, set types.Set, diags *diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return
	}
	var objs []types.Object
	diags.Append(set.ElementsAs(ctx, &objs, false)...)
	seen := map[string]bool{}
	for _, o := range objs {
		if o.IsUnknown() || o.IsNull() {
			continue
		}
		attrs := o.Attributes()
		at := path.Root("grant")
		rv, _ := strAttr(attrs, "right")
		if rv.IsUnknown() {
			continue
		}
		right, ok := grants.Catalog.ByName[rv.ValueString()]
		if !ok {
			diags.AddAttributeError(at, "Unbekanntes Recht",
				fmt.Sprintf("%q steht nicht im Rechte-Katalog (ChurchTools %s). Schreibweise wie in "+
					"ct.config.ts, z. B. \"churchdb:view alldata\".", rv.ValueString(), grants.Catalog.Version))
			continue
		}
		if kind.authIDBelow > 0 && right.AuthID >= kind.authIDBelow {
			diags.AddAttributeError(at, "Recht an dieser Domäne nicht erlaubt",
				fmt.Sprintf("%s (authId %d): ChurchTools erlaubt an der Domäne %s nur Rechte mit authId < %d.",
					right.Name, right.AuthID, kind.domain, kind.authIDBelow))
			continue
		}
		subgroups := types.BoolNull()
		if b, ok := attrs["applies_to_subgroups"].(types.Bool); ok {
			subgroups = b
		}
		if !subgroups.IsNull() && !subgroups.IsUnknown() && !subgroups.ValueBool() {
			diags.AddAttributeError(at, "applies_to_subgroups = false weglassen",
				fmt.Sprintf("%s: Lass applies_to_subgroups weg statt false zu schreiben. Der gelesene "+
					"Zustand kennt nur „gesetzt“ oder „nicht gesetzt“, false würde in jedem Plan als Änderung erscheinen.",
					right.Name))
		}
		want := grants.ScopeAttrFor(right)
		scoped, unknownScope := false, false
		for _, a := range grants.ScopeAttrs {
			v, _ := attrs[a].(types.Set)
			if v.IsNull() {
				continue
			}
			if v.IsUnknown() {
				unknownScope = true
			}
			if a != want {
				if want == "" {
					diags.AddAttributeError(at, "Recht ohne Auswahl",
						fmt.Sprintf("%s hat keine Daten-Auswahl; %s gehört nicht dazu.", right.Name, a))
				} else {
					diags.AddAttributeError(at, "Falsche Auswahl-Dimension",
						fmt.Sprintf("%s wählt nach %s aus; schreib die ids in %s statt in %s.",
							right.Name, right.ScopeField, want, a))
				}
				continue
			}
			scoped = true
			if v.IsUnknown() {
				continue
			}
			var ids []types.String
			diags.Append(v.ElementsAs(ctx, &ids, false)...)
			if len(ids) == 0 {
				diags.AddAttributeError(at, "Leere Auswahl",
					fmt.Sprintf("%s: %s = [] ist leer. Lass das Argument weg (Recht ohne Auswahl) oder nenne ids.",
						right.Name, a))
			}
			for _, id := range ids {
				if !id.IsUnknown() && !dataIDPattern.MatchString(id.ValueString()) {
					diags.AddAttributeError(at, "Ungültige id",
						fmt.Sprintf("%s: %q ist keine ChurchTools-id (Zahl, oder -1 für alle).", right.Name, id.ValueString()))
				}
			}
		}
		if want != "" && !scoped {
			diags.AddAttributeWarning(at, "Recht ohne Auswahl",
				fmt.Sprintf("%s wählt nach %s aus, der Block nennt aber keine ids. Das schreibt die Zeile "+
					"ohne Auswahl (dataId null), nicht „alle“ — für alle schreib %s = [\"-1\"].",
					right.Name, right.ScopeField, want))
		}
		if subgroups.IsUnknown() || unknownScope {
			continue
		}
		k := grants.Spec{Right: right.Name, Subgroups: subgroups.ValueBool()}
		if scoped {
			k.IDs = []int64{0}
		}
		if seen[k.Key()] {
			diags.AddAttributeError(at, "Recht doppelt deklariert",
				fmt.Sprintf("%s steht in mehr als einem grant-Block. Fasse die ids in einem Block zusammen; "+
					"ChurchTools speichert sie als ein Recht, und der gelesene Zustand hätte nur einen Block.", right.Name))
		}
		seen[k.Key()] = true
	}
}

func validatePreserve(set types.Set, p path.Path, diags *diag.Diagnostics) {
	if set.IsNull() || set.IsUnknown() {
		return
	}
	for _, e := range set.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() {
			continue
		}
		if !grants.Catalog.Dimensions[s.ValueString()] {
			diags.AddAttributeError(p, "Unbekannte Dimension",
				fmt.Sprintf("%q ist keine Auswahl-Dimension des Rechte-Katalogs. Gemeint sind CT-Feldnamen "+
					"wie crp_query, cc_html_template oder cdb_gruppe.", s.ValueString()))
		}
	}
}

// --- model <-> engine ---

func specsFromSet(ctx context.Context, set types.Set) ([]grants.Spec, diag.Diagnostics) {
	var diags diag.Diagnostics
	if set.IsNull() {
		return nil, diags
	}
	var objs []types.Object
	diags.Append(set.ElementsAs(ctx, &objs, false)...)
	if diags.HasError() {
		return nil, diags
	}
	out := make([]grants.Spec, 0, len(objs))
	for _, o := range objs {
		attrs := o.Attributes()
		rv, _ := strAttr(attrs, "right")
		s := grants.Spec{Right: rv.ValueString()}
		if b, ok := attrs["applies_to_subgroups"].(types.Bool); ok {
			s.Subgroups = b.ValueBool()
		}
		for _, a := range grants.ScopeAttrs {
			v, ok := attrs[a].(types.Set)
			if !ok || v.IsNull() {
				continue
			}
			var ids []string
			diags.Append(v.ElementsAs(ctx, &ids, false)...)
			for _, id := range ids {
				n, err := strconv.ParseInt(id, 10, 64)
				if err != nil {
					diags.AddError("Ungültige id", fmt.Sprintf("%s: %q ist keine Zahl", s.Right, id))
					continue
				}
				s.IDs = append(s.IDs, n)
			}
		}
		out = append(out, s)
	}
	return out, diags
}

func rowsFromSet(ctx context.Context, set types.Set) ([]grants.Row, diag.Diagnostics) {
	specs, diags := specsFromSet(ctx, set)
	if diags.HasError() {
		return nil, diags
	}
	rows, err := grants.ExpandSpecs(specs)
	if err != nil {
		diags.AddError("Rechte konnten nicht aufgelöst werden", err.Error())
	}
	return rows, diags
}

func setFromRows(ctx context.Context, kind grantsKind, rows []grants.Row) (types.Set, diag.Diagnostics) {
	var diags diag.Diagnostics
	specs := grants.CollapseRows(rows)
	attrTypes := grantAttrTypesFor(kind.subgroups)
	vals := make([]attr.Value, 0, len(specs))
	for _, s := range specs {
		attrs := map[string]attr.Value{"right": types.StringValue(s.Right)}
		if kind.subgroups {
			attrs["applies_to_subgroups"] = types.BoolNull()
			if s.Subgroups {
				attrs["applies_to_subgroups"] = types.BoolValue(true)
			}
		}
		for _, a := range grants.ScopeAttrs {
			attrs[a] = types.SetNull(types.StringType)
		}
		if len(s.IDs) > 0 {
			ids := make([]attr.Value, len(s.IDs))
			for i, id := range s.IDs {
				ids[i] = types.StringValue(strconv.FormatInt(id, 10))
			}
			a := grants.ScopeAttrFor(grants.Catalog.ByName[s.Right])
			if a == "" {
				// A data row on a right the catalog calls unscoped. Should not happen;
				// keep it visible rather than drop it.
				a = grants.RawDataIDsAttr
			}
			attrs[a] = types.SetValueMust(types.StringType, ids)
		}
		o, d := types.ObjectValue(attrTypes, attrs)
		diags.Append(d...)
		vals = append(vals, o)
	}
	s, d := types.SetValue(grantObjectTypeFor(kind.subgroups), vals)
	diags.Append(d...)
	return s, diags
}

func preservedSet(rows []grants.Row) types.Set {
	grants.SortRows(rows)
	vals := make([]attr.Value, len(rows))
	for i, r := range rows {
		vals[i] = types.StringValue(r.String())
	}
	return types.SetValueMust(types.StringType, vals)
}

func preserveDims(set types.Set) map[string]bool {
	m := map[string]bool{}
	for _, e := range set.Elements() {
		if s, ok := e.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			m[s.ValueString()] = true
		}
	}
	return m
}

// parsePreserveSuffix turns the optional `<dim>,<dim>` import suffix into a
// preserve_unknown value. Import runs Read without the configuration, so
// without it an imported resource would hold its preserved rows in state and
// the first plan would show them as removals.
func parsePreserveSuffix(suffix string, diags *diag.Diagnostics) types.Set {
	var vals []attr.Value
	for _, dim := range strings.Split(suffix, ",") {
		if dim = strings.TrimSpace(dim); dim != "" {
			vals = append(vals, types.StringValue(dim))
		}
	}
	set := types.SetValueMust(types.StringType, vals)
	validatePreserve(set, path.Root("preserve_unknown"), diags)
	return set
}

// preservedGrantsModifier keeps preserved_grants from state on an update that
// cannot change it, so a plan with a grant diff does not also show it as
// "known after apply". The set can change on an update only when
// preserve_unknown changes or the config starts declaring a preserved row;
// then it stays unknown.
type preservedGrantsModifier struct{}

func (preservedGrantsModifier) Description(context.Context) string {
	return "Keeps preserved_grants from state unless preserve_unknown changes or a preserved row is declared."
}

func (m preservedGrantsModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (preservedGrantsModifier) PlanModifySet(ctx context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	if req.StateValue.IsNull() || !req.PlanValue.IsUnknown() || req.Plan.Raw.IsNull() {
		return
	}
	var planGrants, planPreserve, statePreserve types.Set
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("grant"), &planGrants)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("preserve_unknown"), &planPreserve)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("preserve_unknown"), &statePreserve)...)
	if resp.Diagnostics.HasError() || planPreserve.IsUnknown() || planGrants.IsUnknown() {
		return
	}
	if !maps.Equal(preserveDims(planPreserve), preserveDims(statePreserve)) {
		return
	}
	if tv, err := planGrants.ToTerraformValue(ctx); err != nil || !tv.IsFullyKnown() {
		return
	}
	desired, d := rowsFromSet(ctx, planGrants)
	if d.HasError() {
		return // ValidateConfig reports it
	}
	preserved := map[string]bool{}
	for _, e := range req.StateValue.Elements() {
		if s, ok := e.(types.String); ok {
			preserved[s.ValueString()] = true
		}
	}
	for _, r := range desired {
		if preserved[r.String()] {
			return
		}
	}
	resp.PlanValue = req.StateValue
}

// --- flow ---

// grantsService runs the apply/read flow of one resource against one domain.
type grantsService struct {
	client *client.Client
	kind   grantsKind
}

// unofferedRows returns the `absent` rows (declared but not live) whose right
// the host's right table does not offer. It asks the host, once per provider
// instance, and only when something is absent. If the host cannot say, it
// fails closed by skipping nothing: the rows then take the strict path (PUT,
// read-back), which works for every right the host stores, so an unreadable
// table never blocks an ordinary grant. The error is returned for messages.
func (s grantsService) unofferedRows(ctx context.Context, absent []grants.Row) ([]grants.Row, error) {
	if len(absent) == 0 {
		return nil, nil
	}
	offered, err := s.client.OfferedAuthIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("ob %s die Rechte %s anbietet, ließ sich nicht klären, daher wird nichts "+
			"übersprungen: %w", s.client.BaseURL(), grants.RowList(absent), err)
	}
	_, skipped := grants.SplitUnoffered(absent, offered)
	return skipped, nil
}

func (s grantsService) warnSkipped(diags *diag.Diagnostics, label string, skipped []grants.Row) {
	if len(skipped) == 0 {
		return
	}
	grants.SortRows(skipped)
	diags.AddWarning("Rechte auf dieser Instanz nicht angeboten",
		fmt.Sprintf("%s: %s bietet diese Rechte nicht an (das zugehörige Modul ist dort nicht "+
			"aktiv): %s. Sie werden auf dieser Instanz weder gesetzt noch entzogen; der Plan bleibt sauber.",
			label, s.client.BaseURL(), grants.RowList(skipped)))
}

// apply writes `desired` onto the domain, revoking only rows of `prior`, then
// reads back and returns the live rows plus the declared rows it skipped
// because the host does not offer their right.
func (s grantsService) apply(ctx context.Context, id string, prior, desired []grants.Row) (after []client.PermissionRow, skipped []grants.Row, err error) {
	live, err := s.client.PermissionRows(ctx, s.kind.domain, id)
	if err != nil {
		return nil, nil, err
	}
	// Only a declared row that is not stored yet can be one the host lacks;
	// every other row proves its right is offered.
	skipped, tableErr := s.unofferedRows(ctx, grants.AbsentRows(desired, live))
	if len(skipped) > 0 {
		skip := grants.RowSet(skipped)
		var keep []grants.Row
		for _, d := range desired {
			if !skip[d] {
				keep = append(keep, d)
			}
		}
		desired = keep
	}
	w, err := grants.PlanWrites(prior, desired, live)
	if err != nil {
		return nil, nil, err
	}
	// Removal is a DELETE with the row's exact authId, dataId and isInherited, on
	// every domain. group_type_role and status never accept type=revoke (that is
	// a group_role-only deny), so a row there is removed, never overridden.
	for _, d := range w.Deletes {
		if err := s.client.DeletePermission(ctx, s.kind.domain, id, d); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range w.Puts {
		if err := s.client.PutPermission(ctx, s.kind.domain, id, p); err != nil {
			return nil, nil, err
		}
	}
	after, err = s.client.PermissionRows(ctx, s.kind.domain, id)
	if err != nil {
		return nil, nil, fmt.Errorf("Rechte nach dem Schreiben nicht lesbar: %w", err)
	}
	// Rights the host offers keep the strict check: 204 without a stored row is an error.
	if err := grants.VerifyWrites(desired, grants.RemovedRows(prior, desired), after); err != nil {
		if tableErr != nil {
			err = fmt.Errorf("%w %v", err, tableErr)
		}
		return nil, nil, err
	}
	return after, skipped, nil
}

// create writes the declared grants onto domain `id`. `summary` titles an error.
func (s grantsService) create(ctx context.Context, id, label, summary string, d *grantsData, diags *diag.Diagnostics) bool {
	desired, dd := rowsFromSet(ctx, d.Grants)
	diags.Append(dd...)
	if diags.HasError() {
		return false
	}
	// Create never revokes: prior is empty. A live grant the config does not
	// declare is kept by this apply and shows up as a removal in the NEXT plan,
	// where an operator can see it — a create must not revoke silently.
	after, skipped, err := s.apply(ctx, id, nil, desired)
	if err != nil {
		diags.AddError(summary, err.Error())
		return false
	}
	s.warnSkipped(diags, label, skipped)
	res := grants.FilterLive(after, grants.RowSet(desired), preserveDims(d.Preserve))
	if extra := grants.RemovedRows(res.Managed, desired); len(extra) > 0 {
		grants.SortRows(extra)
		diags.AddWarning("Nicht deklarierte Rechte vorhanden",
			fmt.Sprintf("%s hat Rechte, die nicht in der Konfiguration stehen: %s. "+
				"Dieser Lauf hat sie nicht angefasst; der nächste Plan schlägt vor, sie zu entziehen. "+
				"Übernimm sie in die Konfiguration oder nimm ihre Dimension in preserve_unknown auf.",
				label, grants.RowList(extra)))
	}
	d.Preserved = preservedSet(res.Preserved)
	return true
}

// read refreshes d from the live rows of domain `id`.
//
// gone reports a domain that no longer exists (404): the caller removes the
// resource from state. Otherwise ok tells whether d was refreshed.
func (s grantsService) read(ctx context.Context, id, label, summary string, d *grantsData, diags *diag.Diagnostics) (ok, gone bool) {
	prior, dd := rowsFromSet(ctx, d.Grants)
	diags.Append(dd...)
	if diags.HasError() {
		return false, false
	}
	live, err := s.client.PermissionRows(ctx, s.kind.domain, id)
	if isNotFound(err) {
		return false, true
	}
	if err != nil {
		diags.AddError(summary, err.Error())
		return false, false
	}
	var declared map[grants.Row]bool
	if prior != nil {
		declared = grants.RowSet(prior)
	}
	res := grants.FilterLive(live, declared, preserveDims(d.Preserve))
	if len(res.Unknown) > 0 {
		grants.SortRows(res.Unknown)
		diags.AddWarning("Rechte außerhalb des Katalogs",
			fmt.Sprintf("%s hat Rechte, deren authId der Rechte-Katalog (ChurchTools %s) "+
				"nicht kennt: %s. Sie werden weder verwaltet noch entzogen.",
				label, grants.Catalog.Version, grants.RowList(res.Unknown)))
	}
	// A declared row the host does not offer is never live. Keep it in state as
	// declared, or every plan would propose to add it again (and every apply
	// would skip it again): that is the clean plan. Only the host's own right
	// table can tell it from a row deleted in the UI, which stays drift.
	skipped, err := s.unofferedRows(ctx, grants.AbsentRows(prior, live))
	if err != nil {
		diags.AddWarning("Rechte-Tabelle nicht lesbar", err.Error()+
			" Fehlende Rechte erscheinen deshalb im Plan als hinzuzufügen.")
	}
	s.warnSkipped(diags, label, skipped)
	res.Managed = append(res.Managed, skipped...)
	set, dd := setFromRows(ctx, s.kind, res.Managed)
	diags.Append(dd...)
	d.Grants = set
	d.Preserved = preservedSet(res.Preserved)
	return true, false
}

// update reconciles domain `id` from state to plan.
func (s grantsService) update(ctx context.Context, id, label, summary string, plan, state *grantsData, diags *diag.Diagnostics) bool {
	desired, dd := rowsFromSet(ctx, plan.Grants)
	diags.Append(dd...)
	prior, dd := rowsFromSet(ctx, state.Grants)
	diags.Append(dd...)
	if diags.HasError() {
		return false
	}
	prior, adopted := grants.AdoptNewlyPreserved(prior, desired, preserveDims(state.Preserve), preserveDims(plan.Preserve))
	if len(adopted) > 0 {
		grants.SortRows(adopted)
		diags.AddWarning("Rechte nicht entzogen",
			fmt.Sprintf("Diese Rechte standen im Plan als Entzug, liegen aber auf einer Dimension, die dieser Lauf "+
				"neu in preserve_unknown aufnimmt; sie bleiben bestehen und erscheinen unter preserved_grants: %s.",
				grants.RowList(adopted)))
	}
	after, skipped, err := s.apply(ctx, id, prior, desired)
	if err != nil {
		diags.AddError(summary, err.Error())
		return false
	}
	s.warnSkipped(diags, label, skipped)
	if kept := grants.RetainedBaseline(prior, desired, after); len(kept) > 0 {
		grants.SortRows(kept)
		diags.AddWarning("System-Rechte nicht entzogen",
			fmt.Sprintf("%s: diese Rechte sind ChurchTools-Systemzeilen (modifiedPid -1) und stehen nicht mehr "+
				"in der Konfiguration. Der Provider entzieht Systemzeilen nie; sie bleiben in ChurchTools "+
				"bestehen und verschwinden nur aus dem State: %s.", label, grants.RowList(kept)))
	}
	res := grants.FilterLive(after, grants.RowSet(desired), preserveDims(plan.Preserve))
	// A known planned value came from preservedGrantsModifier and must be
	// returned as planned; it can only differ when the UI changed the domain
	// between plan and apply, and the next refresh picks that up.
	if plan.Preserved.IsUnknown() {
		plan.Preserved = preservedSet(res.Preserved)
	}
	return true
}

func grantsDeleteWarning(diags *diag.Diagnostics, whose string) {
	diags.AddWarning(orphanOnDeleteSummary,
		fmt.Sprintf("Die Rechte %s werden von diesem Provider beim Entfernen der Ressource nicht entzogen. "+
			"Die Ressource wurde nur aus dem Terraform-State entfernt; die Rechte gelten in ChurchTools weiter. "+
			"Wer sie entziehen will, entfernt zuerst die grant-Blöcke und wendet das an.", whose))
}

// isNotFound reports a 404 from CT, however the client wrapped it.
func isNotFound(err error) bool {
	if errors.Is(err, client.ErrNotFound) {
		return true
	}
	_, ok := client.IsStatus(err, http.StatusNotFound)
	return ok
}
