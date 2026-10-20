package grants

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
)

// Unit tests of the grant engine over payloads captured read-only from
// eqrm-dev on 2026-10-19 (testdata/group_role). No TF_ACC, no network.

func fixture(t *testing.T, name string) []client.PermissionRow {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.DecodePermissionRows(raw)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func i64(v int64) *int64 { return &v }

func live(authID int64, data *int64, inherited bool) client.PermissionRow {
	return client.PermissionRow{AuthID: authID, DataID: data, IsInherited: inherited, Type: "grant"}
}

func baseline(r client.PermissionRow) client.PermissionRow {
	pid := int64(-1)
	r.Meta = &struct {
		ModifiedPid *int64 `json:"modifiedPid"`
	}{&pid}
	return r
}

const (
	seePersons   = int64(10101) // cc_securitylevel
	viewQuery    = int64(702)   // crp_query
	useTemplates = int64(17)    // cc_html_template
	viewAlldata  = int64(102)   // cdb_bereich
	adminTmpl    = int64(16)    // unscoped
)

func TestCatalogEmbedded(t *testing.T) {
	if len(Catalog.ByID) != 187 || Catalog.Version == "" {
		t.Fatalf("catalog: %d rights, version %q", len(Catalog.ByID), Catalog.Version)
	}
	for _, d := range []string{"crp_query", "cc_html_template", "cdb_gruppe", "cc_securitylevel"} {
		if !Catalog.Dimensions[d] {
			t.Errorf("dimension %s missing", d)
		}
	}
}

// Every row of the real fixtures is known to the catalog, survives a
// filter→collapse→expand round trip, and the inherited rows stay distinct
// from the direct ones (#25).
func TestFixturesRoundTrip(t *testing.T) {
	for _, name := range []string{"14853_skidscheckinopsmz_mitglied", "16829_sll_koblenz_mitglied", "area_administration_leiter"} {
		t.Run(name, func(t *testing.T) {
			rows := fixture(t, name)
			res := FilterLive(rows, nil, nil)
			if len(res.Unknown) > 0 || len(res.Preserved) > 0 {
				t.Fatalf("unknown %v preserved %v", res.Unknown, res.Preserved)
			}
			if len(res.Managed) != len(rows) {
				t.Fatalf("managed %d of %d rows", len(res.Managed), len(rows))
			}
			back, err := ExpandSpecs(CollapseRows(res.Managed))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(RowSet(back), RowSet(res.Managed)) {
				t.Fatalf("round trip changed rows:\n%v\n%v", back, res.Managed)
			}
		})
	}
}

func TestInheritedIsItsOwnBlock(t *testing.T) {
	res := FilterLive(fixture(t, "14853_skidscheckinopsmz_mitglied"), nil, nil)
	var direct, sub bool
	var inheritedOnly bool
	for _, s := range CollapseRows(res.Managed) {
		if s.Right == "churchdb:+see persons" {
			if s.Subgroups {
				sub = true
			} else {
				direct = true
			}
		}
		if s.Right == "churchdb:+edit group member fields" {
			inheritedOnly = s.Subgroups
		}
	}
	if !direct || !sub {
		t.Fatalf("10101 [3] must be two blocks (direct %v, subgroups %v)", direct, sub)
	}
	if !inheritedOnly {
		t.Fatal("10133 exists only inherited on dev and must read as applies_to_subgroups")
	}
}

// Diff stability: reading the same live rows with the previous read as
// "declared" gives the same state, three times over, with or without a
// preserved row present.
func TestReadIsStable(t *testing.T) {
	rows := append(fixture(t, "16829_sll_koblenz_mitglied"), live(viewQuery, i64(5), false))
	preserve := map[string]bool{"crp_query": true}
	var prev []Spec
	declared := map[Row]bool{}
	for i := 0; i < 3; i++ {
		res := FilterLive(rows, declared, preserve)
		specs := CollapseRows(res.Managed)
		if i > 0 && !reflect.DeepEqual(specs, prev) {
			t.Fatalf("pass %d differs:\n%v\n%v", i, specs, prev)
		}
		if len(res.Preserved) != 1 {
			t.Fatalf("pass %d: preserved %v", i, res.Preserved)
		}
		prev = specs
		declared = RowSet(res.Managed)
	}
}

func TestFilterLiveRows(t *testing.T) {
	declaredQuery := Row{AuthID: viewQuery, HasData: true, DataID: 1}
	rows := []client.PermissionRow{
		live(viewAlldata, i64(2), false),                      // non-preserved, undeclared: managed
		live(viewQuery, i64(1), false),                        // preserved dim, declared: managed
		live(viewQuery, i64(9), false),                        // preserved dim, undeclared: preserved
		live(viewQuery, i64(9), true),                         // inherited twin: preserved, distinct
		live(useTemplates, i64(4), false),                     // cc_html_template, not in preserve: managed
		live(adminTmpl, nil, false),                           // unscoped: never preserved
		baseline(live(seePersons, i64(1), false)),             // CT's own row: skipped
		{AuthID: viewAlldata, DataID: i64(3), Type: "revoke"}, // deny: skipped
		live(999999, nil, false),                              // unknown authId
		live(viewAlldata, i64(2), false),                      // duplicate
	}
	res := FilterLive(rows, map[Row]bool{declaredQuery: true}, map[string]bool{"crp_query": true})
	want := ReadResult{
		Managed: []Row{
			{AuthID: viewAlldata, HasData: true, DataID: 2},
			declaredQuery,
			{AuthID: useTemplates, HasData: true, DataID: 4},
			{AuthID: adminTmpl},
		},
		Preserved: []Row{
			{AuthID: viewQuery, HasData: true, DataID: 9},
			{AuthID: viewQuery, HasData: true, DataID: 9, Subgroups: true},
		},
		Unknown: []Row{{AuthID: 999999}},
	}
	if !reflect.DeepEqual(res, want) {
		t.Fatalf("got  %+v\nwant %+v", res, want)
	}
}

// The core promise. Config declares 102 [2]; live also holds an undeclared
// crp_query row (preserved) and an undeclared 124 row (not preserved). Read →
// state; the diff config-vs-state removes 124 only; the apply's writes never
// touch 702.
func TestUndeclaredPreservedNeverRevoked(t *testing.T) {
	rows := append(fixture(t, "16829_sll_koblenz_mitglied"),
		live(viewQuery, i64(7), false), live(viewQuery, nil, false))
	desired := []Row{{AuthID: viewAlldata, HasData: true, DataID: 2}}
	preserve := map[string]bool{"crp_query": true}

	res := FilterLive(rows, RowSet(desired), preserve)
	planned := RemovedRows(res.Managed, desired)
	if len(planned) != 1 || planned[0] != (Row{AuthID: 124, HasData: true, DataID: 24}) {
		t.Fatalf("planned removals %v, want only 124 [24]", planned)
	}
	for _, p := range res.Preserved {
		if p.AuthID != viewQuery {
			t.Fatalf("preserved %v", p)
		}
	}

	w, err := PlanWrites(res.Managed, desired, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Puts) != 0 {
		t.Fatalf("puts %v", w.Puts)
	}
	if !reflect.DeepEqual(w.Deletes, []client.PermissionWrite{{AuthID: 124, DataID: []int64{24}}}) {
		t.Fatalf("deletes %+v", w.Deletes)
	}

	// Even a prior state that somehow held the preserved row cannot revoke the
	// UNSCOPED-by-accident null row of a scoped right through a wide DELETE
	// while selection rows exist.
	_, err = PlanWrites([]Row{{AuthID: viewQuery}}, nil, rows)
	if err == nil || !strings.Contains(err.Error(), "Auswahl-Zeile") {
		t.Fatalf("wide delete not refused: %v", err)
	}
}

// Without preserve_unknown the same undeclared crp_query row IS planned.
func TestUndeclaredNonPreservedPlanned(t *testing.T) {
	rows := []client.PermissionRow{live(viewQuery, i64(7), false), live(useTemplates, i64(3), false)}
	res := FilterLive(rows, nil, map[string]bool{"cc_html_template": true})
	planned := RemovedRows(res.Managed, nil)
	if len(planned) != 1 || planned[0].AuthID != viewQuery {
		t.Fatalf("planned %v, want only 702 [7]", planned)
	}
	w, err := PlanWrites(res.Managed, nil, rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.Deletes, []client.PermissionWrite{{AuthID: viewQuery, DataID: []int64{7}}}) {
		t.Fatalf("deletes %+v", w.Deletes)
	}
}

func TestPlanGrantWritesPuts(t *testing.T) {
	desired := []Row{
		{AuthID: viewAlldata, HasData: true, DataID: 2},
		{AuthID: viewAlldata, HasData: true, DataID: 5},
		{AuthID: viewAlldata, HasData: true, DataID: 5, Subgroups: true},
		{AuthID: adminTmpl},
	}
	w, err := PlanWrites(nil, desired, fixture(t, "16829_sll_koblenz_mitglied"))
	if err != nil {
		t.Fatal(err)
	}
	want := []client.PermissionWrite{
		{AuthID: viewAlldata, DataID: []int64{5}, Type: "grant"},
		{AuthID: viewAlldata, DataID: []int64{5}, IsInherited: true, Type: "grant"},
		{AuthID: adminTmpl, Type: "grant"},
	}
	if !reflect.DeepEqual(w.Puts, want) || len(w.Deletes) != 0 {
		t.Fatalf("puts %+v deletes %+v", w.Puts, w.Deletes)
	}
}

func TestPlanGrantWritesGuards(t *testing.T) {
	t.Run("deny is not flipped", func(t *testing.T) {
		rows := []client.PermissionRow{{AuthID: viewAlldata, DataID: i64(2), Type: "revoke"}}
		_, err := PlanWrites(nil, []Row{{AuthID: viewAlldata, HasData: true, DataID: 2}}, rows)
		if err == nil || !strings.Contains(err.Error(), "Verweigerung") {
			t.Fatal(err)
		}
	})
	t.Run("-1 swallows selections", func(t *testing.T) {
		rows := []client.PermissionRow{live(viewAlldata, i64(-1), false)}
		_, err := PlanWrites(nil, []Row{{AuthID: viewAlldata, HasData: true, DataID: 2}}, rows)
		if err == nil || !strings.Contains(err.Error(), "-1") {
			t.Fatal(err)
		}
	})
	t.Run("baseline never deleted", func(t *testing.T) {
		row := baseline(live(seePersons, i64(1), false))
		w, err := PlanWrites([]Row{RowOf(row)}, nil, []client.PermissionRow{row})
		if err != nil || len(w.Deletes) != 0 {
			t.Fatalf("%v %+v", err, w.Deletes)
		}
	})
	t.Run("null row alone deletes without dataId", func(t *testing.T) {
		rows := []client.PermissionRow{live(adminTmpl, nil, false)}
		w, err := PlanWrites([]Row{{AuthID: adminTmpl}}, nil, rows)
		if err != nil || !reflect.DeepEqual(w.Deletes, []client.PermissionWrite{{AuthID: adminTmpl}}) {
			t.Fatalf("%v %+v", err, w.Deletes)
		}
	})
	t.Run("-1 narrowed to an id: delete -1, then put", func(t *testing.T) {
		rows := []client.PermissionRow{live(viewAlldata, i64(-1), false)}
		all := Row{AuthID: viewAlldata, HasData: true, DataID: -1}
		two := Row{AuthID: viewAlldata, HasData: true, DataID: 2}
		w, err := PlanWrites([]Row{all}, []Row{two}, rows)
		if err != nil {
			t.Fatal(err)
		}
		wantDel := []client.PermissionWrite{{AuthID: viewAlldata, DataID: []int64{-1}}}
		wantPut := []client.PermissionWrite{{AuthID: viewAlldata, DataID: []int64{2}, Type: "grant"}}
		if !reflect.DeepEqual(w.Deletes, wantDel) || !reflect.DeepEqual(w.Puts, wantPut) {
			t.Fatalf("%+v %+v", w.Deletes, w.Puts)
		}
	})
	t.Run("null row and its id rows removed together", func(t *testing.T) {
		rows := []client.PermissionRow{live(viewAlldata, nil, false), live(viewAlldata, i64(5), false)}
		prior := []Row{{AuthID: viewAlldata}, {AuthID: viewAlldata, HasData: true, DataID: 5}}
		w, err := PlanWrites(prior, nil, rows)
		want := []client.PermissionWrite{{AuthID: viewAlldata, DataID: []int64{5}}, {AuthID: viewAlldata}}
		if err != nil || !reflect.DeepEqual(w.Deletes, want) {
			t.Fatalf("%v %+v", err, w.Deletes)
		}
	})
	t.Run("null row next to a kept id row is refused", func(t *testing.T) {
		rows := []client.PermissionRow{live(viewAlldata, nil, false), live(viewAlldata, i64(5), false)}
		prior := []Row{{AuthID: viewAlldata}, {AuthID: viewAlldata, HasData: true, DataID: 5}}
		_, err := PlanWrites(prior, prior[1:], rows)
		if err == nil || !strings.Contains(err.Error(), "einzeln") {
			t.Fatal(err)
		}
	})
	t.Run("gone already: nothing sent", func(t *testing.T) {
		w, err := PlanWrites([]Row{{AuthID: adminTmpl}}, nil, nil)
		if err != nil || len(w.Deletes) != 0 {
			t.Fatalf("%v %+v", err, w.Deletes)
		}
	})
}

func TestVerifyGrantWrites(t *testing.T) {
	desired := []Row{{AuthID: 701}, {AuthID: viewAlldata, HasData: true, DataID: 2}}
	err := VerifyWrites(desired, nil, fixture(t, "16829_sll_koblenz_mitglied"))
	if err == nil || !strings.Contains(err.Error(), "churchreport:view") || !strings.Contains(err.Error(), "nicht gespeichert") {
		t.Fatalf("missing 701 not reported: %v", err)
	}
	err = VerifyWrites(nil, []Row{{AuthID: 124, HasData: true, DataID: 24}}, fixture(t, "16829_sll_koblenz_mitglied"))
	if err == nil || !strings.Contains(err.Error(), "noch vorhanden") {
		t.Fatalf("stuck row not reported: %v", err)
	}
	if err := VerifyWrites([]Row{{AuthID: viewAlldata, HasData: true, DataID: 2}}, nil, fixture(t, "16829_sll_koblenz_mitglied")); err != nil {
		t.Fatal(err)
	} // A declared baseline row dropped from config is left in CT on purpose
	// (PlanWrites skips it), so the read-back must not call it stuck.
	row := baseline(live(seePersons, i64(1), false))
	if err := VerifyWrites(nil, []Row{RowOf(row)}, []client.PermissionRow{row}); err != nil {
		t.Fatalf("baseline reported as stuck: %v", err)
	}
}
