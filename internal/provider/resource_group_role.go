package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

const groupRoleDomain = "group_role"

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

type grantModel struct {
	Right              types.String `tfsdk:"right"`
	AppliesToSubgroups types.Bool   `tfsdk:"applies_to_subgroups"`
	Groups             types.Set    `tfsdk:"groups"`
	Campuses           types.Set    `tfsdk:"campuses"`
	Departments        types.Set    `tfsdk:"departments"`
	SecurityLevels     types.Set    `tfsdk:"security_levels"`
	GroupTypes         types.Set    `tfsdk:"group_types"`
	CommentViewers     types.Set    `tfsdk:"comment_viewers"`
	DataIDs            types.Set    `tfsdk:"data_ids"`
}

func (g *grantModel) scope() map[string]*types.Set {
	return map[string]*types.Set{
		"groups":          &g.Groups,
		"campuses":        &g.Campuses,
		"departments":     &g.Departments,
		"security_levels": &g.SecurityLevels,
		"group_types":     &g.GroupTypes,
		"comment_viewers": &g.CommentViewers,
		rawDataIDsAttr:    &g.DataIDs,
	}
}

var grantAttrTypes = func() map[string]attr.Type {
	m := map[string]attr.Type{
		"right":                types.StringType,
		"applies_to_subgroups": types.BoolType,
	}
	for _, a := range scopeAttrs {
		m[a] = types.SetType{ElemType: types.StringType}
	}
	return m
}()

var grantObjectType = types.ObjectType{AttrTypes: grantAttrTypes}

func NewGroupRoleResource() resource.Resource { return &groupRoleResource{} }

var (
	_ resource.ResourceWithValidateConfig = &groupRoleResource{}
	_ resource.ResourceWithImportState    = &groupRoleResource{}
)

func (r *groupRoleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_role"
}

func (r *groupRoleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	idSet := func(desc string) schema.SetAttribute {
		return schema.SetAttribute{ElementType: types.StringType, Optional: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Die Rechte einer Rolle in einer Gruppe („Rechte verwalten“ an der Gruppe). " +
			"Jedes nicht deklarierte Recht wird entzogen, außer auf den Dimensionen in preserve_unknown.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The (group, role) pairing id: the `group_role` permission domain.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"group_id": schema.StringAttribute{
				Required:      true,
				Description:   "Id of the group, e.g. churchtools_group.x.id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"role": schema.StringAttribute{
				Required: true,
				Description: "Role name as ChurchTools stores it (`name` on /groups/{id}/roles, e.g. " +
					"\"Mitglied\", \"Leiter\", \"participant\"), not the translated label.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
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
			},
		},
		Blocks: map[string]schema.Block{
			"grant": schema.SetNestedBlock{
				Description: "One right. Without a scope argument it is the right's row without a data " +
					"selection; with one, one row per id.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"right": schema.StringAttribute{
							Required:    true,
							Description: "Right as `module:name` from CT's permission catalog, e.g. \"churchdb:view alldata\".",
						},
						"applies_to_subgroups": schema.BoolAttribute{
							Optional: true,
							Description: "true: the row CT marks isInherited, which applies in the SUBGROUPS of " +
								"the group, not in the group itself. Left out: the group itself. Never write false.",
						},
						"groups":          idSet("Group ids (scope cdb_gruppe)."),
						"campuses":        idSet("Campus ids (scope cdb_station). -1 = all."),
						"departments":     idSet("Department ids (scope cdb_bereich). -1 = all."),
						"security_levels": idSet("Security levels (scope cc_securitylevel)."),
						"group_types":     idSet("Group type ids (scope cdb_gruppentyp)."),
						"comment_viewers": idSet("Comment viewer ids (scope cdb_comment_viewer)."),
						rawDataIDsAttr: idSet("Raw data ids, for every other scope dimension " +
							"(crp_query, cc_html_template, cc_calcategory, …)."),
					},
				},
			},
		},
	}
}

func (r *groupRoleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

var dataIDPattern = regexp.MustCompile(`^(-1|0|[1-9][0-9]*)$`)

// ValidateConfig catches at plan time what would otherwise be a silent wrong
// grant: a scope on the wrong dimension, an explicit false that Read can never
// reproduce, two blocks Read would merge into one.
func (r *groupRoleResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m groupRoleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validatePreserve(m.PreserveUnknown, path.Root("preserve_unknown"), &resp.Diagnostics)

	if m.Grants.IsNull() || m.Grants.IsUnknown() {
		return
	}
	var objs []types.Object
	resp.Diagnostics.Append(m.Grants.ElementsAs(ctx, &objs, false)...)
	seen := map[string]bool{}
	for _, o := range objs {
		if o.IsUnknown() || o.IsNull() {
			continue
		}
		var g grantModel
		resp.Diagnostics.Append(o.As(ctx, &g, basetypes.ObjectAsOptions{})...)
		if resp.Diagnostics.HasError() {
			return
		}
		at := path.Root("grant")
		if g.Right.IsUnknown() {
			continue
		}
		right, ok := rightsCatalog.byName[g.Right.ValueString()]
		if !ok {
			resp.Diagnostics.AddAttributeError(at, "Unbekanntes Recht",
				fmt.Sprintf("%q steht nicht im Rechte-Katalog (ChurchTools %s). Schreibweise wie in "+
					"ct.config.ts, z. B. \"churchdb:view alldata\".", g.Right.ValueString(), rightsCatalog.version))
			continue
		}
		if !g.AppliesToSubgroups.IsNull() && !g.AppliesToSubgroups.IsUnknown() && !g.AppliesToSubgroups.ValueBool() {
			resp.Diagnostics.AddAttributeError(at, "applies_to_subgroups = false weglassen",
				fmt.Sprintf("%s: Lass applies_to_subgroups weg statt false zu schreiben. Der gelesene "+
					"Zustand kennt nur „gesetzt“ oder „nicht gesetzt“, false würde in jedem Plan als Änderung erscheinen.",
					right.Name))
		}
		want := scopeAttrFor(right)
		scoped, unknownScope := false, false
		for _, a := range scopeAttrs {
			v := *g.scope()[a]
			if v.IsNull() {
				continue
			}
			if v.IsUnknown() {
				unknownScope = true
			}
			if a != want {
				if want == "" {
					resp.Diagnostics.AddAttributeError(at, "Recht ohne Auswahl",
						fmt.Sprintf("%s hat keine Daten-Auswahl; %s gehört nicht dazu.", right.Name, a))
				} else {
					resp.Diagnostics.AddAttributeError(at, "Falsche Auswahl-Dimension",
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
			resp.Diagnostics.Append(v.ElementsAs(ctx, &ids, false)...)
			if len(ids) == 0 {
				resp.Diagnostics.AddAttributeError(at, "Leere Auswahl",
					fmt.Sprintf("%s: %s = [] ist leer. Lass das Argument weg (Recht ohne Auswahl) oder nenne ids.",
						right.Name, a))
			}
			for _, id := range ids {
				if !id.IsUnknown() && !dataIDPattern.MatchString(id.ValueString()) {
					resp.Diagnostics.AddAttributeError(at, "Ungültige id",
						fmt.Sprintf("%s: %q ist keine ChurchTools-id (Zahl, oder -1 für alle).", right.Name, id.ValueString()))
				}
			}
		}
		if want != "" && !scoped {
			resp.Diagnostics.AddAttributeWarning(at, "Recht ohne Auswahl",
				fmt.Sprintf("%s wählt nach %s aus, der Block nennt aber keine ids. Das schreibt die Zeile "+
					"ohne Auswahl (dataId null), nicht „alle“ — für alle schreib %s = [\"-1\"].",
					right.Name, right.ScopeField, want))
		}
		if g.AppliesToSubgroups.IsUnknown() || unknownScope {
			continue
		}
		k := grantSpec{Right: right.Name, Subgroups: g.AppliesToSubgroups.ValueBool()}
		if scoped {
			k.IDs = []int64{0}
		}
		if seen[k.specKey()] {
			resp.Diagnostics.AddAttributeError(at, "Recht doppelt deklariert",
				fmt.Sprintf("%s steht in mehr als einem grant-Block. Fasse die ids in einem Block zusammen; "+
					"ChurchTools speichert sie als ein Recht, und der gelesene Zustand hätte nur einen Block.", right.Name))
		}
		seen[k.specKey()] = true
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
		if !rightsCatalog.dimensions[s.ValueString()] {
			diags.AddAttributeError(p, "Unbekannte Dimension",
				fmt.Sprintf("%q ist keine Auswahl-Dimension des Rechte-Katalogs. Gemeint sind CT-Feldnamen "+
					"wie crp_query, cc_html_template oder cdb_gruppe.", s.ValueString()))
		}
	}
}

// --- model <-> engine ---

func specsFromSet(ctx context.Context, set types.Set) ([]grantSpec, diag.Diagnostics) {
	var diags diag.Diagnostics
	if set.IsNull() {
		return nil, diags
	}
	var gs []grantModel
	diags.Append(set.ElementsAs(ctx, &gs, false)...)
	if diags.HasError() {
		return nil, diags
	}
	out := make([]grantSpec, 0, len(gs))
	for _, g := range gs {
		s := grantSpec{Right: g.Right.ValueString(), Subgroups: g.AppliesToSubgroups.ValueBool()}
		for _, a := range scopeAttrs {
			v := *g.scope()[a]
			if v.IsNull() {
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

func rowsFromSet(ctx context.Context, set types.Set) ([]grantRow, diag.Diagnostics) {
	specs, diags := specsFromSet(ctx, set)
	if diags.HasError() {
		return nil, diags
	}
	rows, err := expandSpecs(specs)
	if err != nil {
		diags.AddError("Rechte konnten nicht aufgelöst werden", err.Error())
	}
	return rows, diags
}

func setFromRows(ctx context.Context, rows []grantRow) (types.Set, diag.Diagnostics) {
	var diags diag.Diagnostics
	specs := collapseRows(rows)
	vals := make([]attr.Value, 0, len(specs))
	for _, s := range specs {
		g := grantModel{Right: types.StringValue(s.Right), AppliesToSubgroups: types.BoolNull()}
		if s.Subgroups {
			g.AppliesToSubgroups = types.BoolValue(true)
		}
		for _, a := range scopeAttrs {
			*g.scope()[a] = types.SetNull(types.StringType)
		}
		if len(s.IDs) > 0 {
			ids := make([]attr.Value, len(s.IDs))
			for i, id := range s.IDs {
				ids[i] = types.StringValue(strconv.FormatInt(id, 10))
			}
			a := scopeAttrFor(rightsCatalog.byName[s.Right])
			if a == "" {
				// A data row on a right the catalog calls unscoped. Should not happen;
				// keep it visible rather than drop it.
				a = rawDataIDsAttr
			}
			*g.scope()[a] = types.SetValueMust(types.StringType, ids)
		}
		o, d := types.ObjectValueFrom(ctx, grantAttrTypes, g)
		diags.Append(d...)
		vals = append(vals, o)
	}
	s, d := types.SetValue(grantObjectType, vals)
	diags.Append(d...)
	return s, diags
}

func preservedSet(rows []grantRow) types.Set {
	sortRows(rows)
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

// apply writes `desired` onto the pairing, revoking only rows of `prior`, then
// reads back and returns the live rows.
func (r *groupRoleResource) apply(ctx context.Context, id string, prior, desired []grantRow) ([]client.PermissionRow, error) {
	live, err := r.client.PermissionRows(ctx, groupRoleDomain, id)
	if err != nil {
		return nil, err
	}
	w, err := planGrantWrites(prior, desired, live)
	if err != nil {
		return nil, err
	}
	for _, d := range w.Deletes {
		if err := r.client.DeletePermission(ctx, groupRoleDomain, id, d); err != nil {
			return nil, err
		}
	}
	for _, p := range w.Puts {
		if err := r.client.PutPermission(ctx, groupRoleDomain, id, p); err != nil {
			return nil, err
		}
	}
	after, err := r.client.PermissionRows(ctx, groupRoleDomain, id)
	if err != nil {
		return nil, fmt.Errorf("Rechte nach dem Schreiben nicht lesbar: %w", err)
	}
	if err := verifyGrantWrites(desired, removedRows(prior, desired), after); err != nil {
		return nil, err
	}
	return after, nil
}

func (r *groupRoleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan groupRoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	desired, d := rowsFromSet(ctx, plan.Grants)
	resp.Diagnostics.Append(d...)
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
	// Create never revokes: prior is empty. A live grant the config does not
	// declare is kept by this apply and shows up as a removal in the NEXT plan,
	// where an operator can see it — a create must not revoke silently.
	after, err := r.apply(ctx, id, nil, desired)
	if err != nil {
		resp.Diagnostics.AddError(summary, err.Error())
		return
	}
	res := filterLiveRows(after, rowSet(desired), preserveDims(plan.PreserveUnknown))
	if extra := removedRows(res.Managed, desired); len(extra) > 0 {
		sortRows(extra)
		resp.Diagnostics.AddWarning("Nicht deklarierte Rechte vorhanden",
			fmt.Sprintf("Rolle %q in Gruppe %s hat Rechte, die nicht in der Konfiguration stehen: %s. "+
				"Dieser Lauf hat sie nicht angefasst; der nächste Plan schlägt vor, sie zu entziehen. "+
				"Übernimm sie in die Konfiguration oder nimm ihre Dimension in preserve_unknown auf.",
				plan.Role.ValueString(), plan.GroupID.ValueString(), rowList(extra)))
	}
	plan.ID = types.StringValue(id)
	plan.PreservedGrants = preservedSet(res.Preserved)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupRoleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state groupRoleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	prior, d := rowsFromSet(ctx, state.Grants)
	resp.Diagnostics.Append(d...)
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
	live, err := r.client.PermissionRows(ctx, groupRoleDomain, id)
	if err != nil {
		resp.Diagnostics.AddError("Rollenrechte konnten nicht gelesen werden", err.Error())
		return
	}
	var declared map[grantRow]bool
	if prior != nil {
		declared = rowSet(prior)
	}
	res := filterLiveRows(live, declared, preserveDims(state.PreserveUnknown))
	if len(res.Unknown) > 0 {
		sortRows(res.Unknown)
		resp.Diagnostics.AddWarning("Rechte außerhalb des Katalogs",
			fmt.Sprintf("Rolle %q in Gruppe %s hat Rechte, deren authId der Rechte-Katalog (ChurchTools %s) "+
				"nicht kennt: %s. Sie werden weder verwaltet noch entzogen.",
				role, state.GroupID.ValueString(), rightsCatalog.version, rowList(res.Unknown)))
	}
	grants, d := setFromRows(ctx, res.Managed)
	resp.Diagnostics.Append(d...)
	state.ID = types.StringValue(id)
	state.Role = types.StringValue(role)
	state.Grants = grants
	state.PreservedGrants = preservedSet(res.Preserved)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *groupRoleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state groupRoleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	desired, d := rowsFromSet(ctx, plan.Grants)
	resp.Diagnostics.Append(d...)
	prior, d := rowsFromSet(ctx, state.Grants)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	prior, adopted := adoptNewlyPreserved(prior, desired, preserveDims(state.PreserveUnknown), preserveDims(plan.PreserveUnknown))
	if len(adopted) > 0 {
		sortRows(adopted)
		resp.Diagnostics.AddWarning("Rechte nicht entzogen",
			fmt.Sprintf("Diese Rechte standen im Plan als Entzug, liegen aber auf einer Dimension, die dieser Lauf "+
				"neu in preserve_unknown aufnimmt; sie bleiben bestehen und erscheinen unter preserved_grants: %s.",
				rowList(adopted)))
	}
	after, err := r.apply(ctx, id, prior, desired)
	if err != nil {
		resp.Diagnostics.AddError("Rollenrechte konnten nicht geändert werden", err.Error())
		return
	}
	res := filterLiveRows(after, rowSet(desired), preserveDims(plan.PreserveUnknown))
	plan.ID = types.StringValue(id)
	plan.PreservedGrants = preservedSet(res.Preserved)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete un-manages without revoking, like every resource here and like
// removing a declaration in ct-cli. Revoking a role's grants is done by
// removing the grant blocks first, which the plan shows row by row.
func (r *groupRoleResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary,
		"Die Rechte dieser Rolle werden von diesem Provider beim Entfernen der Ressource nicht entzogen. "+
			"Die Ressource wurde nur aus dem Terraform-State entfernt; die Rechte gelten in ChurchTools weiter. "+
			"Wer sie entziehen will, entfernt zuerst die grant-Blöcke und wendet das an.")
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
		var vals []attr.Value
		for _, dim := range strings.Split(parts[2], ",") {
			if dim = strings.TrimSpace(dim); dim != "" {
				vals = append(vals, types.StringValue(dim))
			}
		}
		preserve = types.SetValueMust(types.StringType, vals)
		validatePreserve(preserve, path.Root("preserve_unknown"), &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("preserve_unknown"), preserve)...)
}

func rowList(rows []grantRow) string {
	s := make([]string, len(rows))
	for i, r := range rows {
		s[i] = r.String()
	}
	return strings.Join(s, ", ")
}

// adoptNewlyPreserved takes out of `prior` the rows that are about to be
// revoked only because they were in state before their dimension joined
// preserve_unknown. Such a row reached state as an undeclared row (a pending
// removal); adding its dimension to preserve_unknown is the operator saying
// "leave those alone", so the apply must not revoke it on that very run.
// Ambiguous case, resolved in the safe direction: a block removed in the same
// change that adds its dimension is kept, not revoked.
func adoptNewlyPreserved(prior, desired []grantRow, before, after map[string]bool) (kept, adopted []grantRow) {
	want := rowSet(desired)
	for _, p := range prior {
		dim := rightsCatalog.byID[p.AuthID].ScopeField
		if !want[p] && dim != "" && after[dim] && !before[dim] {
			adopted = append(adopted, p)
			continue
		}
		kept = append(kept, p)
	}
	return kept, adopted
}
