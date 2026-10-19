package provider

// The grant engine behind churchtools_group_role: pure functions over CT's raw
// permission rows, kept free of framework types so they can be tested against
// captured payloads (testdata/group_role/) without Terraform in the loop.
//
// Vocabulary, because two of these words mean something else in ct-cli:
//
//   - row       one stored assignment, identity (authId, dataId, isInherited).
//     CT stores one row per dataId; dataId is absent for a right without a
//     data selection ("null row").
//   - grant     one `grant {}` block: a right, whether it applies in the group or
//     its subgroups, and zero or more ids. It expands to one row per id, or to
//     the null row when it names no ids.
//   - dimension a right's scope field from CT's permission catalog
//     (`cdb_gruppe`, `cdb_station`, `crp_query`, …). preserve_unknown is a set
//     of dimensions.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
)

// The catalog is ChurchTools' right table (`churchauth/ajax getMasterData` →
// auth_table), captured from eqrm.church.tools with `ct permissions catalog
// --refresh`. It is the only source for name ↔ authId ↔ dimension: the REST API
// has no endpoint for it. Embedded rather than fetched per host so that a plan
// is deterministic and testable; see the PR's open questions.
//
//go:embed permission_catalog.json
var permissionCatalogJSON []byte

type catalogRight struct {
	Name       string
	AuthID     int64
	ScopeField string // "" for a right without a data selection
}

type permissionCatalog struct {
	byName     map[string]catalogRight
	byID       map[int64]catalogRight
	dimensions map[string]bool
	version    string
}

func loadPermissionCatalog(raw []byte) (*permissionCatalog, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	c := &permissionCatalog{byName: map[string]catalogRight{}, byID: map[int64]catalogRight{}, dimensions: map[string]bool{}}
	for name, msg := range entries {
		if name == "$meta" {
			var meta struct {
				CtVersion string `json:"ctVersion"`
			}
			_ = json.Unmarshal(msg, &meta)
			c.version = meta.CtVersion
			continue
		}
		var e struct {
			AuthID     int64   `json:"authId"`
			ScopeField *string `json:"scopeField"`
		}
		if err := json.Unmarshal(msg, &e); err != nil {
			return nil, fmt.Errorf("catalog entry %q: %w", name, err)
		}
		r := catalogRight{Name: name, AuthID: e.AuthID}
		if e.ScopeField != nil {
			r.ScopeField = *e.ScopeField
			c.dimensions[r.ScopeField] = true
		}
		if prev, dup := c.byID[r.AuthID]; dup {
			return nil, fmt.Errorf("catalog: authId %d is both %q and %q", r.AuthID, prev.Name, name)
		}
		c.byName[name] = r
		c.byID[r.AuthID] = r
	}
	return c, nil
}

var rightsCatalog = func() *permissionCatalog {
	c, err := loadPermissionCatalog(permissionCatalogJSON)
	if err != nil {
		panic("embedded permission catalog: " + err.Error())
	}
	return c
}()

// Scope dimensions that get a named argument in a grant block. Every other
// dimension (crp_query, cc_html_template, cc_calcategory, …) is written as raw
// `data_ids`: ct-cli has no host-independent name for them either.
var scopeAttrByDimension = map[string]string{
	"cdb_gruppe":         "groups",
	"cdb_station":        "campuses",
	"cdb_bereich":        "departments",
	"cc_securitylevel":   "security_levels",
	"cdb_gruppentyp":     "group_types",
	"cdb_comment_viewer": "comment_viewers",
}

const rawDataIDsAttr = "data_ids"

// scopeAttrs is every scope argument, in schema order.
var scopeAttrs = []string{"groups", "campuses", "departments", "security_levels", "group_types", "comment_viewers", rawDataIDsAttr}

// scopeAttrFor names the argument a right's ids belong in, "" for an unscoped right.
func scopeAttrFor(r catalogRight) string {
	if r.ScopeField == "" {
		return ""
	}
	if a, ok := scopeAttrByDimension[r.ScopeField]; ok {
		return a
	}
	return rawDataIDsAttr
}

// grantRow is a row's identity, comparable so it can key a map.
type grantRow struct {
	AuthID    int64
	HasData   bool
	DataID    int64
	Subgroups bool
}

func rowOf(r client.PermissionRow) grantRow {
	g := grantRow{AuthID: r.AuthID, Subgroups: r.IsInherited}
	if r.DataID != nil {
		g.HasData, g.DataID = true, *r.DataID
	}
	return g
}

func (g grantRow) String() string {
	name := strconv.FormatInt(g.AuthID, 10)
	if r, ok := rightsCatalog.byID[g.AuthID]; ok {
		name = r.Name
	}
	s := name
	if g.HasData {
		s += " [" + strconv.FormatInt(g.DataID, 10) + "]"
	}
	if g.Subgroups {
		s += " (Untergruppen)"
	}
	return s
}

// grantSpec is one `grant {}` block as plain values.
type grantSpec struct {
	Right     string
	Subgroups bool
	IDs       []int64 // empty: the null row
}

// specKey is what makes two blocks the same block. Read can only produce one
// block per key, so config must not split a key across two blocks either.
func (s grantSpec) specKey() string {
	return fmt.Sprintf("%s|%t|%t", s.Right, s.Subgroups, len(s.IDs) > 0)
}

func expandSpecs(specs []grantSpec) ([]grantRow, error) {
	var out []grantRow
	for _, s := range specs {
		r, ok := rightsCatalog.byName[s.Right]
		if !ok {
			return nil, fmt.Errorf("unbekanntes Recht %q", s.Right)
		}
		if len(s.IDs) == 0 {
			out = append(out, grantRow{AuthID: r.AuthID, Subgroups: s.Subgroups})
			continue
		}
		for _, id := range s.IDs {
			out = append(out, grantRow{AuthID: r.AuthID, HasData: true, DataID: id, Subgroups: s.Subgroups})
		}
	}
	return out, nil
}

// collapseRows is expandSpecs' inverse, in a canonical order: one block per
// (right, subgroups, has-ids), ids sorted. Rows whose authId the catalog cannot
// name never reach here (filterLiveRows drops them).
func collapseRows(rows []grantRow) []grantSpec {
	byKey := map[string]*grantSpec{}
	for _, g := range rows {
		r := rightsCatalog.byID[g.AuthID]
		s := grantSpec{Right: r.Name, Subgroups: g.Subgroups}
		if g.HasData {
			s.IDs = []int64{g.DataID}
		}
		k := s.specKey()
		if cur, ok := byKey[k]; ok {
			cur.IDs = append(cur.IDs, s.IDs...)
			continue
		}
		cp := s
		byKey[k] = &cp
	}
	out := make([]grantSpec, 0, len(byKey))
	for _, s := range byKey {
		sort.Slice(s.IDs, func(i, j int) bool { return s.IDs[i] < s.IDs[j] })
		s.IDs = dedupeIDs(s.IDs)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].specKey() < out[j].specKey() })
	return out
}

func dedupeIDs(ids []int64) []int64 {
	out := ids[:0]
	for i, id := range ids {
		if i == 0 || id != ids[i-1] {
			out = append(out, id)
		}
	}
	return out
}

// readResult is what Read keeps, and what it deliberately leaves out.
type readResult struct {
	Managed   []grantRow // goes into state as grant blocks
	Preserved []grantRow // undeclared rows on a preserve_unknown dimension
	Unknown   []grantRow // authIds the catalog cannot name: left alone, warned about
}

// filterLiveRows decides which live rows the resource's state shows.
//
// `declared` is the set of rows the previous state held (nil on import). It is
// what tells "a preserved-dimension row I own" from "one somebody added by
// hand", which a dimension alone cannot.
//
// Left out of state, and therefore never planned for removal:
//
//   - deny rows (type "revoke"): an admin's explicit deny is never owned, as in
//     ct-cli's diffGrants;
//   - CT's system baseline (modifiedPid -1), unless it was declared: CT re-adds
//     those itself, and a declared one counts as satisfied (ct-cli #114);
//   - authIds missing from the catalog;
//   - rows on a preserve_unknown dimension that were not declared.
//
// Everything else is in state, so an undeclared row on any other dimension —
// or an undeclared unscoped right — shows up in the plan as a removal.
func filterLiveRows(live []client.PermissionRow, declared map[grantRow]bool, preserve map[string]bool) readResult {
	var res readResult
	seen := map[grantRow]bool{}
	for _, lr := range live {
		if lr.Type != "grant" {
			continue
		}
		g := rowOf(lr)
		if seen[g] {
			continue
		}
		seen[g] = true
		r, known := rightsCatalog.byID[g.AuthID]
		switch {
		case !known:
			res.Unknown = append(res.Unknown, g)
		case declared[g]:
			res.Managed = append(res.Managed, g)
		case lr.IsSystemBaseline():
			// undeclared baseline: never ours
		case r.ScopeField != "" && preserve[r.ScopeField]:
			res.Preserved = append(res.Preserved, g)
		default:
			res.Managed = append(res.Managed, g)
		}
	}
	return res
}

// grantWrites is the request list one apply sends, in order: deletes first,
// so a re-scoped grant never briefly holds both selections.
type grantWrites struct {
	Deletes []client.PermissionWrite
	Puts    []client.PermissionWrite
}

type writeKey struct {
	AuthID    int64
	Subgroups bool
}

// planGrantWrites turns (previous state, desired, live) into requests.
//
// The rule that makes preserve_unknown safe: a DELETE is only ever sent for a
// row that was in the previous state — i.e. a row the plan showed — and is
// absent from the desired set. A row Read filtered out (a preserved one, a
// deny, the baseline) can never be revoked by an apply, whatever the config
// says, because it is never in `prior`.
//
// PUTs go out for every desired row that is not live yet, which also repairs a
// row deleted in the UI when the run skipped refresh.
func planGrantWrites(prior, desired []grantRow, live []client.PermissionRow) (grantWrites, error) {
	liveGrant := map[grantRow]client.PermissionRow{}
	liveDeny := map[grantRow]bool{}
	liveData := map[writeKey][]grantRow{} // every live row with a dataId, any type
	for _, lr := range live {
		g := rowOf(lr)
		if lr.Type == "grant" {
			liveGrant[g] = lr
		} else {
			liveDeny[g] = true
		}
		if g.HasData {
			k := writeKey{g.AuthID, g.Subgroups}
			liveData[k] = append(liveData[k], g)
		}
	}
	want := map[grantRow]bool{}
	for _, d := range desired {
		want[d] = true
	}

	var w grantWrites

	// Deletes.
	delIDs := map[writeKey][]int64{}
	var delNull []writeKey
	for _, p := range prior {
		if want[p] {
			continue
		}
		lr, isLive := liveGrant[p]
		if !isLive || lr.IsSystemBaseline() {
			continue // already gone, or CT's own row: drop it from state, send nothing
		}
		k := writeKey{p.AuthID, p.Subgroups}
		if p.HasData {
			delIDs[k] = append(delIDs[k], p.DataID)
			continue
		}
		// DELETE without dataId removes every selection row of this key too, so it
		// is only safe when the null row is the key's only row.
		if others := liveData[k]; len(others) > 0 {
			return w, fmt.Errorf("%s kann nicht einzeln entzogen werden: ChurchTools löscht mit dieser "+
				"Zeile auch %d Auswahl-Zeile(n) desselben Rechts (z. B. %s). Entziehe sie in der "+
				"ChurchTools-Oberfläche", p, len(others), others[0])
		}
		delNull = append(delNull, k)
	}
	for _, k := range sortedWriteKeys(delIDs) {
		ids := delIDs[k]
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		w.Deletes = append(w.Deletes, client.PermissionWrite{AuthID: k.AuthID, DataID: ids, IsInherited: k.Subgroups})
	}
	sortWriteKeys(delNull)
	for _, k := range delNull {
		w.Deletes = append(w.Deletes, client.PermissionWrite{AuthID: k.AuthID, IsInherited: k.Subgroups})
	}

	// Puts.
	putIDs := map[writeKey][]int64{}
	var putNull []writeKey
	for _, d := range desired {
		if _, ok := liveGrant[d]; ok {
			continue
		}
		if liveDeny[d] {
			return w, fmt.Errorf("%s steht in ChurchTools als ausdrückliche Verweigerung (Typ revoke). "+
				"Ein PUT würde sie stillschweigend in eine Erlaubnis umdrehen; kläre das in der "+
				"ChurchTools-Oberfläche", d)
		}
		k := writeKey{d.AuthID, d.Subgroups}
		if d.HasData {
			putIDs[k] = append(putIDs[k], d.DataID)
		} else {
			putNull = append(putNull, k)
		}
	}
	for _, k := range sortedWriteKeys(putIDs) {
		// CT ignores a non-empty PUT entirely while a -1 ("alle") row exists for
		// the key (OpenAPI, PUT /permissions/…). Say so instead of failing later
		// on the read-back.
		if _, all := liveGrant[grantRow{AuthID: k.AuthID, HasData: true, DataID: -1, Subgroups: k.Subgroups}]; all {
			return w, fmt.Errorf("%s hat in ChurchTools bereits die Auswahl -1 (alle); ChurchTools "+
				"ignoriert dann jede weitere Auswahl. Nimm -1 in die Konfiguration auf oder entferne es zuerst",
				grantRow{AuthID: k.AuthID, Subgroups: k.Subgroups})
		}
		ids := putIDs[k]
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		w.Puts = append(w.Puts, client.PermissionWrite{AuthID: k.AuthID, DataID: ids, IsInherited: k.Subgroups, Type: "grant"})
	}
	sortWriteKeys(putNull)
	for _, k := range putNull {
		w.Puts = append(w.Puts, client.PermissionWrite{AuthID: k.AuthID, IsInherited: k.Subgroups, Type: "grant"})
	}
	return w, nil
}

// verifyGrantWrites is the read-back after an apply. CT answers 204 to a PUT
// it did not store — a right of a module the host lacks (dev's 701/702/2080),
// or a selection next to an existing -1 — so a 2xx proves nothing.
func verifyGrantWrites(desired, removed []grantRow, live []client.PermissionRow) error {
	liveGrant := map[grantRow]bool{}
	for _, lr := range live {
		if lr.Type == "grant" {
			liveGrant[rowOf(lr)] = true
		}
	}
	var missing, stuck []string
	for _, d := range desired {
		if !liveGrant[d] {
			missing = append(missing, d.String())
		}
	}
	for _, r := range removed {
		if liveGrant[r] {
			stuck = append(stuck, r.String())
		}
	}
	if len(missing) == 0 && len(stuck) == 0 {
		return nil
	}
	var b strings.Builder
	if len(missing) > 0 {
		fmt.Fprintf(&b, "ChurchTools hat diese Rechte angenommen (204), aber nicht gespeichert: %s. "+
			"Häufigste Ursache: das Modul des Rechts ist auf dieser Instanz nicht aktiv "+
			"(auf eqrm-dev z. B. churchreport und jpmFlowManagerPreview).", strings.Join(missing, ", "))
	}
	if len(stuck) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "Diese Rechte sind nach dem Entziehen noch vorhanden: %s.", strings.Join(stuck, ", "))
	}
	return fmt.Errorf("%s", b.String())
}

// removedRows is prior minus desired: what an apply meant to revoke.
func removedRows(prior, desired []grantRow) []grantRow {
	want := map[grantRow]bool{}
	for _, d := range desired {
		want[d] = true
	}
	var out []grantRow
	for _, p := range prior {
		if !want[p] {
			out = append(out, p)
		}
	}
	return out
}

func rowSet(rows []grantRow) map[grantRow]bool {
	m := make(map[grantRow]bool, len(rows))
	for _, r := range rows {
		m[r] = true
	}
	return m
}

func sortedWriteKeys(m map[writeKey][]int64) []writeKey {
	keys := make([]writeKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortWriteKeys(keys)
	return keys
}

func sortWriteKeys(keys []writeKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].AuthID != keys[j].AuthID {
			return keys[i].AuthID < keys[j].AuthID
		}
		return !keys[i].Subgroups && keys[j].Subgroups
	})
}

func sortRows(rows []grantRow) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].String() < rows[j].String() })
}
