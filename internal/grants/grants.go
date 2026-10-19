// Package grants is the grant engine behind every resource that owns a
// permission domain (group_role, group_type_role, status): pure functions
// over CT's raw permission rows, kept free of framework types so they can be tested against
// captured payloads (testdata/) without Terraform in the loop.
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
package grants

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
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

type Right struct {
	Name       string
	AuthID     int64
	ScopeField string // "" for a right without a data selection
}

type PermissionCatalog struct {
	ByName     map[string]Right
	ByID       map[int64]Right
	Dimensions map[string]bool
	Version    string
}

func LoadCatalog(raw []byte) (*PermissionCatalog, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	c := &PermissionCatalog{ByName: map[string]Right{}, ByID: map[int64]Right{}, Dimensions: map[string]bool{}}
	for name, msg := range entries {
		if name == "$meta" {
			var meta struct {
				CtVersion string `json:"ctVersion"`
			}
			_ = json.Unmarshal(msg, &meta)
			c.Version = meta.CtVersion
			continue
		}
		var e struct {
			AuthID     int64   `json:"authId"`
			ScopeField *string `json:"scopeField"`
		}
		if err := json.Unmarshal(msg, &e); err != nil {
			return nil, fmt.Errorf("catalog entry %q: %w", name, err)
		}
		r := Right{Name: name, AuthID: e.AuthID}
		if e.ScopeField != nil {
			r.ScopeField = *e.ScopeField
			c.Dimensions[r.ScopeField] = true
		}
		if prev, dup := c.ByID[r.AuthID]; dup {
			return nil, fmt.Errorf("catalog: authId %d is both %q and %q", r.AuthID, prev.Name, name)
		}
		c.ByName[name] = r
		c.ByID[r.AuthID] = r
	}
	return c, nil
}

var Catalog = func() *PermissionCatalog {
	c, err := LoadCatalog(permissionCatalogJSON)
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

const RawDataIDsAttr = "data_ids"

// ScopeAttrs is every scope argument, in schema order.
var ScopeAttrs = []string{"groups", "campuses", "departments", "security_levels", "group_types", "comment_viewers", RawDataIDsAttr}

// ScopeAttrFor names the argument a right's ids belong in, "" for an unscoped right.
func ScopeAttrFor(r Right) string {
	if r.ScopeField == "" {
		return ""
	}
	if a, ok := scopeAttrByDimension[r.ScopeField]; ok {
		return a
	}
	return RawDataIDsAttr
}

// Row is a row's identity, comparable so it can key a map.
type Row struct {
	AuthID    int64
	HasData   bool
	DataID    int64
	Subgroups bool
}

func RowOf(r client.PermissionRow) Row {
	g := Row{AuthID: r.AuthID, Subgroups: r.IsInherited}
	if r.DataID != nil {
		g.HasData, g.DataID = true, *r.DataID
	}
	return g
}

func (g Row) String() string {
	name := strconv.FormatInt(g.AuthID, 10)
	if r, ok := Catalog.ByID[g.AuthID]; ok {
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

// Spec is one `grant {}` block as plain values.
type Spec struct {
	Right     string
	Subgroups bool
	IDs       []int64 // empty: the null row
}

// Key is what makes two blocks the same block. Read can only produce one
// block per key, so config must not split a key across two blocks either.
func (s Spec) Key() string {
	return fmt.Sprintf("%s|%t|%t", s.Right, s.Subgroups, len(s.IDs) > 0)
}

func ExpandSpecs(specs []Spec) ([]Row, error) {
	var out []Row
	for _, s := range specs {
		r, ok := Catalog.ByName[s.Right]
		if !ok {
			return nil, fmt.Errorf("unbekanntes Recht %q", s.Right)
		}
		if len(s.IDs) == 0 {
			out = append(out, Row{AuthID: r.AuthID, Subgroups: s.Subgroups})
			continue
		}
		for _, id := range s.IDs {
			out = append(out, Row{AuthID: r.AuthID, HasData: true, DataID: id, Subgroups: s.Subgroups})
		}
	}
	return out, nil
}

// CollapseRows is ExpandSpecs' inverse, in a canonical order: one block per
// (right, subgroups, has-ids), ids sorted. Rows whose authId the catalog cannot
// name never reach here (FilterLive drops them).
func CollapseRows(rows []Row) []Spec {
	byKey := map[string]*Spec{}
	for _, g := range rows {
		r := Catalog.ByID[g.AuthID]
		s := Spec{Right: r.Name, Subgroups: g.Subgroups}
		if g.HasData {
			s.IDs = []int64{g.DataID}
		}
		k := s.Key()
		if cur, ok := byKey[k]; ok {
			cur.IDs = append(cur.IDs, s.IDs...)
			continue
		}
		cp := s
		byKey[k] = &cp
	}
	out := make([]Spec, 0, len(byKey))
	for _, s := range byKey {
		sort.Slice(s.IDs, func(i, j int) bool { return s.IDs[i] < s.IDs[j] })
		s.IDs = dedupeIDs(s.IDs)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
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

// ReadResult is what Read keeps, and what it deliberately leaves out.
type ReadResult struct {
	Managed   []Row // goes into state as grant blocks
	Preserved []Row // undeclared rows on a preserve_unknown dimension
	Unknown   []Row // authIds the catalog cannot name: left alone, warned about
}

// FilterLive decides which live rows the resource's state shows.
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
func FilterLive(live []client.PermissionRow, declared map[Row]bool, preserve map[string]bool) ReadResult {
	var res ReadResult
	seen := map[Row]bool{}
	for _, lr := range live {
		if lr.Type != "grant" {
			continue
		}
		g := RowOf(lr)
		if seen[g] {
			continue
		}
		seen[g] = true
		r, known := Catalog.ByID[g.AuthID]
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

// Writes is the request list one apply sends, in order: deletes first,
// so a re-scoped grant never briefly holds both selections.
type Writes struct {
	Deletes []client.PermissionWrite
	Puts    []client.PermissionWrite
}

type writeKey struct {
	AuthID    int64
	Subgroups bool
}

// PlanWrites turns (previous state, desired, live) into requests.
//
// The rule that makes preserve_unknown safe: a DELETE is only ever sent for a
// row that was in the previous state — i.e. a row the plan showed — and is
// absent from the desired set. A row Read filtered out (a preserved one, a
// deny, the baseline) can never be revoked by an apply, whatever the config
// says, because it is never in `prior`.
//
// PUTs go out for every desired row that is not live yet, which also repairs a
// row deleted in the UI when the run skipped refresh.
func PlanWrites(prior, desired []Row, live []client.PermissionRow) (Writes, error) {
	liveGrant := map[Row]client.PermissionRow{}
	liveDeny := map[Row]bool{}
	liveData := map[writeKey][]Row{} // every live row with a dataId, any type
	for _, lr := range live {
		g := RowOf(lr)
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
	want := map[Row]bool{}
	for _, d := range desired {
		want[d] = true
	}

	var w Writes

	// Deletes.
	delIDs := map[writeKey][]int64{}
	var delNull []writeKey
	var nullRows []Row
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
		nullRows = append(nullRows, p)
	}
	// DELETE without dataId removes every selection row of this key too, so it
	// is only safe when the key keeps no selection row once this apply's id
	// deletes (sent first) have run.
	for _, p := range nullRows {
		k := writeKey{p.AuthID, p.Subgroups}
		var others []Row
		for _, o := range liveData[k] {
			if !slices.Contains(delIDs[k], o.DataID) {
				others = append(others, o)
			}
		}
		if len(others) > 0 {
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
		// A -1 this apply deletes first does not count.
		all := Row{AuthID: k.AuthID, HasData: true, DataID: -1, Subgroups: k.Subgroups}
		if _, allLive := liveGrant[all]; allLive && !slices.Contains(delIDs[k], -1) {
			if want[all] {
				return w, fmt.Errorf("%s: die Auswahl -1 (alle) und einzelne Auswahlen schließen sich aus, "+
					"ChurchTools ignoriert neben -1 jede weitere. Deklariere nur eines von beiden",
					Row{AuthID: k.AuthID, Subgroups: k.Subgroups})
			}
			return w, fmt.Errorf("%s hat in ChurchTools bereits die Auswahl -1 (alle); ChurchTools "+
				"ignoriert dann jede weitere Auswahl. Nimm -1 in die Konfiguration auf oder entferne es zuerst",
				Row{AuthID: k.AuthID, Subgroups: k.Subgroups})
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

// VerifyWrites is the read-back after an apply. CT answers 204 to a PUT
// it did not store — a right of a module the host lacks (dev's 701/702/2080),
// or a selection next to an existing -1 — so a 2xx proves nothing.
func VerifyWrites(desired, removed []Row, live []client.PermissionRow) error {
	liveGrant := map[Row]bool{}
	baseline := map[Row]bool{}
	for _, lr := range live {
		if lr.Type == "grant" {
			liveGrant[RowOf(lr)] = true
			baseline[RowOf(lr)] = lr.IsSystemBaseline()
		}
	}
	var missing, stuck []string
	for _, d := range desired {
		if !liveGrant[d] {
			missing = append(missing, d.String())
		}
	}
	for _, r := range removed {
		// PlanWrites leaves CT's baseline rows alone on purpose.
		if liveGrant[r] && !baseline[r] {
			stuck = append(stuck, r.String())
		}
	}
	if len(missing) == 0 && len(stuck) == 0 {
		return nil
	}
	var b strings.Builder
	if len(missing) > 0 {
		fmt.Fprintf(&b, "ChurchTools hat diese Rechte angenommen (204), aber nicht gespeichert: %s.",
			strings.Join(missing, ", "))
	}
	if len(stuck) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "Diese Rechte sind nach dem Entziehen noch vorhanden: %s.", strings.Join(stuck, ", "))
	}
	return fmt.Errorf("%s", b.String())
}

// AbsentRows are the rows of `want` that no live grant row carries.
func AbsentRows(want []Row, live []client.PermissionRow) []Row {
	have := map[Row]bool{}
	for _, lr := range live {
		if lr.Type == "grant" {
			have[RowOf(lr)] = true
		}
	}
	var out []Row
	for _, w := range want {
		if !have[w] {
			out = append(out, w)
		}
	}
	return out
}

// SplitUnoffered separates rows whose right the host does not offer (its own
// right table, client.OfferedAuthIDs) from the rest. The unoffered ones are
// never written, never read back and never revoked: CT answers 204 to such a
// PUT and stores nothing, so there is nothing to manage on that host.
func SplitUnoffered(rows []Row, offered map[int64]bool) (keep, skipped []Row) {
	for _, r := range rows {
		if offered[r.AuthID] {
			keep = append(keep, r)
		} else {
			skipped = append(skipped, r)
		}
	}
	return keep, skipped
}

// RemovedRows is prior minus desired: what an apply meant to revoke.
func RemovedRows(prior, desired []Row) []Row {
	want := map[Row]bool{}
	for _, d := range desired {
		want[d] = true
	}
	var out []Row
	for _, p := range prior {
		if !want[p] {
			out = append(out, p)
		}
	}
	return out
}

func RowSet(rows []Row) map[Row]bool {
	m := make(map[Row]bool, len(rows))
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

func SortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].String() < rows[j].String() })
}

func RowList(rows []Row) string {
	s := make([]string, len(rows))
	for i, r := range rows {
		s[i] = r.String()
	}
	return strings.Join(s, ", ")
}

// AdoptNewlyPreserved takes out of `prior` the rows that are about to be
// revoked only because they were in state before their dimension joined
// preserve_unknown. Such a row reached state as an undeclared row (a pending
// removal); adding its dimension to preserve_unknown is the operator saying
// "leave those alone", so the apply must not revoke it on that very run.
// Ambiguous case, resolved in the safe direction: a block removed in the same
// change that adds its dimension is kept, not revoked.
func AdoptNewlyPreserved(prior, desired []Row, before, after map[string]bool) (kept, adopted []Row) {
	want := RowSet(desired)
	for _, p := range prior {
		dim := Catalog.ByID[p.AuthID].ScopeField
		if !want[p] && dim != "" && after[dim] && !before[dim] {
			adopted = append(adopted, p)
			continue
		}
		kept = append(kept, p)
	}
	return kept, adopted
}
