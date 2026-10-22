package ruleset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, s string) any {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return v
}

func canon(t *testing.T, v any) string {
	t.Helper()
	s, err := Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func norm(t *testing.T, s string) string {
	t.Helper()
	m, err := Normalize(mustParse(t, s))
	if err != nil {
		t.Fatalf("normalize %s: %v", s, err)
	}
	return canon(t, m)
}

// query wraps an expression in a ruleset so the query-only rules apply.
func normQuery(t *testing.T, expr string) string {
	t.Helper()
	m, err := Normalize(mustParse(t, `{"query":`+expr+`}`))
	if err != nil {
		t.Fatal(err)
	}
	return canon(t, m["query"])
}

// Ported from ct-cli tests/dynamic.test.ts.
func TestStripCosmeticLabels(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"string label unwrapped, recursively",
			`{"and":[{"dterm":["Campus",{"==":[{"var":"ctgroup.campusId"},1]}]}]}`,
			`{"and":[{"==":[{"var":"ctgroup.campusId"},1]}]}`},
		{"title-only object label unwrapped",
			`{"dterm":[{"title":"group.x.title"},{"isnull":[{"var":"person.dateOfDeath"}]}]}`,
			`{"isnull":[{"var":"person.dateOfDeath"}]}`},
		{"null label unwrapped",
			`{"dterm":[null,{"isnull":[{"var":"a"}]}]}`,
			`{"isnull":[{"var":"a"}]}`},
		{"stereotype wrapper kept, inner cosmetic label stripped",
			`{"!":[{"dterm":[{"stereotype":["groupmembership"],"title":"Mitgliedschaft"},{"and":[{"dterm":["Merkmal",{"oneof":[{"var":"ctgroup.id"},["42"]]}]}]}]}]}`,
			`{"!":[{"dterm":[{"stereotype":["groupmembership"],"title":"Mitgliedschaft"},{"and":[{"oneof":[{"var":"ctgroup.id"},[42]]}]}]}]}`},
		{"label with a key besides title is kept",
			`{"dterm":[{"title":"t","hint":"perperson"},{"isnull":[{"var":"person.dateOfDeath"}]}]}`,
			`{"dterm":[{"hint":"perperson","title":"t"},{"isnull":[{"var":"person.dateOfDeath"}]}]}`},
		{"array and number labels are not cosmetic",
			`{"a":{"dterm":[["x"],{"b":1}]},"c":{"dterm":[5,{"d":1}]}}`,
			`{"a":{"dterm":[["x"],{"b":1}]},"c":{"dterm":[5,{"d":1}]}}`},
		{"wrapper with a sibling key is not a wrapper",
			`{"dterm":["x",{"b":1}],"other":1}`,
			`{"dterm":["x",{"b":1}],"other":1}`},
		{"wrapper of wrong arity is not a wrapper",
			`{"dterm":["x"]}`, `{"dterm":["x"]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normQuery(t, c.in); got != canon(t, mustParse(t, c.want)) {
				t.Errorf("got  %s\nwant %s", got, canon(t, mustParse(t, c.want)))
			}
		})
	}
}

func TestCoerceScalars(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"kept label verbatim: numeric title is free text",
			`{"dterm":[{"stereotype":["groupmembership"],"title":"2024"},{"oneof":[{"var":"ctgroup.id"},["5"]]}]}`,
			`{"dterm":[{"stereotype":["groupmembership"],"title":"2024"},{"oneof":[{"var":"ctgroup.id"},[5]]}]}`},
		{"numeric string to number",
			`{"==":[{"var":"ctgroup.campusId"},"1"]}`, `{"==":[{"var":"ctgroup.campusId"},1]}`},
		{"oneof arrays, non-numeric untouched",
			`{"oneof":[{"var":"ctgroup.id"},["112","8"]]}`, `{"oneof":[{"var":"ctgroup.id"},[112,8]]}`},
		{"status word stays",
			`{"==":[{"var":"groupmember.groupMemberStatus"},"active"]}`,
			`{"==":[{"var":"groupmember.groupMemberStatus"},"active"]}`},
		{"leading zero stays a string",
			`{"==":[{"var":"person.zip"},"01067"]}`, `{"==":[{"var":"person.zip"},"01067"]}`},
		{"beyond 2^53 stays a string",
			`{"==":[{"var":"x.id"},"90071992547409910"]}`, `{"==":[{"var":"x.id"},"90071992547409910"]}`},
		{"2^53 itself is not safe",
			`{"a":"9007199254740992"}`, `{"a":"9007199254740992"}`},
		{"2^53-1 is safe",
			`{"a":"9007199254740991"}`, `{"a":9007199254740991}`},
		{"zero and negatives",
			`{"a":["0","-5","-0","+5"," 5","5 ","1e3","1.5",""]}`, `{"a":[0,-5,"-0","+5"," 5","5 ","1e3","1.5",""]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normQuery(t, c.in); got != canon(t, mustParse(t, c.want)) {
				t.Errorf("got  %s\nwant %s", got, canon(t, mustParse(t, c.want)))
			}
		})
	}
}

func TestNormalizeEnvelopesAndTimestamps(t *testing.T) {
	want := `{"description":"x","process":{},"query":{}}`
	for name, in := range map[string]string{
		"bare":            `{"description":"x","process":{},"query":{}}`,
		"GET array":       `[{"description":"x","process":{},"query":{}}]`,
		"PUT envelope":    `{"dynamicGroupRuleSet":[{"description":"x","process":{},"query":{}}]}`,
		"object envelope": `{"dynamicGroupRuleSet":{"description":"x","process":{},"query":{}}}`,
		"timestamps":      `[{"description":"x","dynamicGroupUpdateStarted":"t","dynamicGroupUpdateFinished":"u","process":{},"query":{}}]`,
	} {
		if got := norm(t, in); got != want {
			t.Errorf("%s: got %s want %s", name, got, want)
		}
	}
	if got := norm(t, `[]`); got != `{}` {
		t.Errorf("empty array: %s", got)
	}
	if got := norm(t, `null`); got != `{}` {
		t.Errorf("null: %s", got)
	}
	if _, err := Normalize(mustParse(t, `"text"`)); err == nil {
		t.Error("a string is not a ruleset")
	}
}

func TestNormalizeOnlyTouchesQuery(t *testing.T) {
	in := `{"description":"2024","shorty":"007","personIdFieldName":"person.id",
	  "query":{"==":[{"var":"ctgroup.id"},"112"]},
	  "process":{"x":{"handleMembership":{"groupTypeRoleId":"5"}},"dterm":["a",{"b":"7"}]}}`
	m, err := Normalize(mustParse(t, in))
	if err != nil {
		t.Fatal(err)
	}
	if m["description"] != "2024" || m["shorty"] != "007" {
		t.Errorf("RuleSet-level strings retyped: %v %v", m["description"], m["shorty"])
	}
	if got := canon(t, m["query"]); got != `{"==":[{"var":"ctgroup.id"},112]}` {
		t.Errorf("query: %s", got)
	}
	if got := canon(t, m["process"]); got != `{"dterm":["a",{"b":"7"}],"x":{"handleMembership":{"groupTypeRoleId":"5"}}}` {
		t.Errorf("process was touched: %s", got)
	}
}

func TestNormalizeDoesNotMutateInput(t *testing.T) {
	in := mustParse(t, `[{"query":{"dterm":["x",{"a":"1"}]},"dynamicGroupUpdateStarted":"t"}]`)
	before := canon(t, in)
	if _, err := Normalize(in); err != nil {
		t.Fatal(err)
	}
	if canon(t, in) != before {
		t.Error("Normalize mutated its input")
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	for _, in := range []string{
		`[{"query":{"and":[{"dterm":["L",{"oneof":[{"var":"a"},["1","2"]]}]},{"dterm":[{"stereotype":["groupmembership"],"title":"5"},{"x":"7"}]}]},"description":"9"}]`,
		`{"dynamicGroupRuleSet":[{"query":{"==":[{"var":"person.zip"},"01067"]}}]}`,
		`{}`,
	} {
		once, err := Normalize(mustParse(t, in))
		if err != nil {
			t.Fatal(err)
		}
		twice, err := Normalize(once)
		if err != nil {
			t.Fatal(err)
		}
		if canon(t, once) != canon(t, twice) {
			t.Errorf("not idempotent:\n%s\n%s", canon(t, once), canon(t, twice))
		}
	}
}

func TestBigIDsKeepPrecision(t *testing.T) {
	// float64 would round this to ...992; UseNumber keeps the digits.
	m, err := Normalize(mustParse(t, `{"process":{"id":9007199254740993},"query":{"a":1.0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := canon(t, m["process"]); got != `{"id":9007199254740993}` {
		t.Errorf("precision lost: %s", got)
	}
	if !Equal(mustParse(t, `{"a":1.0,"b":[1e2]}`), mustParse(t, `{"b":[100],"a":1}`)) {
		t.Error("1.0 should equal 1 and 1e2 equal 100")
	}
	if Equal(mustParse(t, `[1,2]`), mustParse(t, `[2,1]`)) {
		t.Error("array order matters")
	}
}

func TestPutBody(t *testing.T) {
	b := PutBody(map[string]any{"description": "x"})
	arr, ok := b["dynamicGroupRuleSet"].([]any)
	if !ok || len(arr) != 1 || len(b) != 1 {
		t.Errorf("PUT envelope wrong: %v", b)
	}
}

func TestMarkersAndResolve(t *testing.T) {
	rs := mustParse(t, `{"query":{"and":[
	  {"==":[{"var":"ctgroup.groupTypeId"},{"__ctRef":true,"kind":"group-type","key":"group"}]},
	  {"oneof":[{"var":"ctgroup.id"},[{"__ctRef":true,"kind":"group","key":"kids"},{"__ctRef":true,"kind":"group","key":"kids"}]]},
	  {"==":[{"var":"person.campusId"},{"__ctRef":true,"kind":"campus","key":"mz"}]},
	  {"==":[{"var":"person.id"},{"__ctRef":true,"kind":"person","key":"p1"}]},
	  {"oneof":[{"var":"role.id"},[{"__ctRef":true,"kind":"group-type-role","groupType":"local_lead","role":"leiter"}]]}]},
	  "process":{"x":{"handleMembership":{"groupTypeRoleId":{"__ctRef":true,"kind":"group-type-role","groupType":"group","role":"member"}}}}}`)
	ms, err := Markers(rs)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, m := range ms {
		keys = append(keys, m.RefKey)
	}
	want := "group-type:group,group:kids,campus:mz,person:p1,group-type-role:local_lead/leiter,group-type-role:group/member"
	// process sorts before query alphabetically: compare as sets
	if len(keys) != 6 {
		t.Fatalf("markers: %v (want %s)", keys, want)
	}
	refs := map[string]string{
		"group-type:group": "7", "group:kids": "42", "campus:mz": "0", "person:p1": "1234",
		"group-type-role:local_lead/leiter": "91", "group-type-role:group/member": "92",
	}
	out, err := Resolve(rs, refs)
	if err != nil {
		t.Fatal(err)
	}
	got := canon(t, out)
	if strings.Contains(got, "__ctRef") {
		t.Fatalf("marker left: %s", got)
	}
	for _, frag := range []string{`"groupTypeRoleId":92`, `[42,42]`, `{"var":"person.campusId"},0]`, `{"var":"person.id"},1234]`, `[91]`} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %s in %s", frag, got)
		}
	}
	if strings.Contains(canon(t, rs), `"groupTypeRoleId":92`) {
		t.Error("Resolve mutated its input")
	}
	delete(refs, "group:kids")
	if _, err := Resolve(rs, refs); err == nil {
		t.Error("missing refs entry must fail")
	}
	refs["group:kids"] = "abc"
	if _, err := Resolve(rs, refs); err == nil {
		t.Error("non-numeric id must fail")
	}
	if _, err := Markers(mustParse(t, `{"a":{"__ctRef":true,"kind":"bogus","key":"x"}}`)); err == nil {
		t.Error("unknown kind must fail")
	}
	if _, err := Markers(mustParse(t, `{"a":{"__ctRef":true,"kind":"group"}}`)); err == nil {
		t.Error("marker without key must fail")
	}
	// A non-marker object that merely has a __ctRef key set to something else is data.
	if ms, _ := Markers(mustParse(t, `{"a":{"__ctRef":"no"}}`)); len(ms) != 0 {
		t.Error("only __ctRef:true is a marker")
	}
}

// -- parity with ct-cli's normalizeRuleset ------------------------------------

// checkParity runs the Go normalizer over every <name>.ruleset.json in dir and
// requires its canonical JSON to be byte-identical to <name>.golden.json, which
// internal/ruleset/testdata/gen-golden.mts produced with ct-cli's real
// normalizeRuleset. It returns how many files matched.
func checkParity(t *testing.T, dir string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.ruleset.json"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		golden, err := os.ReadFile(strings.TrimSuffix(f, ".ruleset.json") + ".golden.json")
		if err != nil {
			t.Errorf("%s: no golden: %v", filepath.Base(f), err)
			continue
		}
		m, err := NormalizeJSON(string(raw))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		got := canon(t, m)
		if got != string(golden) {
			t.Errorf("%s: Go normalizer differs from ct-cli", filepath.Base(f))
			continue
		}
		// idempotence on real data
		again, _ := Normalize(m)
		if canon(t, again) != got {
			t.Errorf("%s: not idempotent", filepath.Base(f))
		}
		n++
	}
	return n
}

func TestParityAnonymizedFixtures(t *testing.T) {
	n := checkParity(t, filepath.Join("testdata", "live"))
	if n == 0 {
		t.Fatal("no fixtures found")
	}
	t.Logf("%d/%d anonymized live rulesets byte-identical to ct-cli", n, n)
}

func TestParityEdgeCases(t *testing.T) {
	if n := checkParity(t, filepath.Join("testdata", "edge")); n == 0 {
		t.Fatal("no edge fixtures found")
	}
}

// TestParityPrivateCorpus re-runs the parity check over the full, non-anonymized
// live corpus. Point CT_RULESET_CORPUS at a directory holding <name>.ruleset.json
// (raw GET bodies) next to <name>.golden.json (gen-golden.mts output). The corpus
// holds real group data and never goes into this repository.
func TestParityPrivateCorpus(t *testing.T) {
	dir := os.Getenv("CT_RULESET_CORPUS")
	if dir == "" {
		t.Skip("CT_RULESET_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.ruleset.json"))
	n := checkParity(t, dir)
	t.Logf("PARITY %d/%d live rulesets byte-identical to ct-cli", n, len(files))
	if n != len(files) || n == 0 {
		t.Fatalf("only %d of %d matched", n, len(files))
	}
}
