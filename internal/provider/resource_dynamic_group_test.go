package provider_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/ruleset"
	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const dgAddr = "churchtools_dynamic_group.d"

func dgConfig(host, groupID, status, ruleset, refs string) string {
	r := ""
	if refs != "" {
		r = "  refs = {\n" + refs + "\n  }\n"
	}
	return providerBlock(host) + fmt.Sprintf(`
resource "churchtools_dynamic_group" "d" {
  group_id = %q
  status   = %q
  ruleset  = %s
%s}
`, groupID, status, ruleset, r)
}

func jsonLit(s string) string { return "<<-EOT\n" + s + "\nEOT" }

func seedGroup(mock *testmock.Server, id, statusID int) {
	mock.Seed("/groups", id, map[string]any{"name": "G", "information": map[string]any{
		"groupTypeId": float64(11), "groupStatusId": float64(statusID), "campusId": nil}})
}

const baseRuleset = `{"description":"d","query":{"method":"ChurchQuery","params":{"filter":{"oneof":[{"var":"ctgroup.id"},[5,6]]}}},"process":{"groupOnly":{"active":"none"}}}`

// Create, import, ruleset update, a status-only change (no ruleset PUT), destroy.
func TestAccDynamicGroup_Lifecycle(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 500, 1)

	puts := func(wantRS, wantSt int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if rs, st := mock.DynamicPuts(500); rs != wantRS || st != wantSt {
				t.Errorf("PUT ruleset/status = %d/%d, want %d/%d", rs, st, wantRS, wantSt)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: dgConfig(mock.URL, "500", "active", jsonLit(baseRuleset), ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(dgAddr, "id", "500"),
					resource.TestCheckResourceAttr(dgAddr, "status", "active"),
					puts(1, 1),
					func(*terraform.State) error {
						if ops := mock.DynamicOps(500); !reflect.DeepEqual(ops, []string{"ruleset", "status"}) {
							t.Errorf("active: PUT order = %v, want ruleset first", ops)
						}
						body := mock.LastRulesetPut(500)
						arr, ok := body["dynamicGroupRuleSet"].([]any)
						if !ok || len(arr) != 1 || len(body) != 1 {
							t.Errorf("PUT envelope = %v", body)
						}
						return nil
					},
				),
			},
			{
				// The imported ruleset is the live JSON in canonical form: same tree, other text.
				ResourceName: dgAddr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"ruleset"},
				ImportStateCheck: func(st []*terraform.InstanceState) error {
					if len(st) != 1 || st[0].Attributes["status"] != "active" || st[0].Attributes["group_id"] != "500" {
						return fmt.Errorf("imported state = %v", st)
					}
					got, err := ruleset.NormalizeJSON(st[0].Attributes["ruleset"])
					want, _ := ruleset.NormalizeJSON(baseRuleset)
					if err != nil || !ruleset.Equal(got, want) {
						return fmt.Errorf("imported ruleset differs: %s", st[0].Attributes["ruleset"])
					}
					return nil
				},
			},
			{
				Config: dgConfig(mock.URL, "500", "active", jsonLit(`{"description":"d2","query":{},"process":{}}`), ""),
				Check:  resource.ComposeTestCheckFunc(puts(2, 1)),
			},
			{
				Config: dgConfig(mock.URL, "500", "manual", jsonLit(`{"description":"d2","query":{},"process":{}}`), ""),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(dgAddr, "status", "manual"),
					puts(2, 2), // status only: the ruleset is not rewritten
					func(*terraform.State) error {
						if _, st := mock.Dynamic(500); st != "manual" {
							t.Errorf("status = %q", st)
						}
						return nil
					},
				),
			},
		},
	})
	if rs, _ := mock.Dynamic(500); rs == nil {
		t.Error("destroy must leave the rule in ChurchTools")
	}
}

// Markers are replaced by INTEGER ids from refs, in query and in
// process.*.handleMembership.groupTypeRoleId; a ref to a resource created in the same
// run is unknown at plan time and must not error; the next plan is empty.
func TestAccDynamicGroup_Refs(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 501, 1)

	rs := `{"description":"d","query":{"method":"ChurchQuery","params":{"filter":{"and":[
  {"==":[{"var":"ctgroup.groupTypeId"},{"__ctRef":true,"kind":"group-type","key":"group"}]},
  {"oneof":[{"var":"ctgroup.id"},[{"__ctRef":true,"kind":"group","key":"other"}]]},
  {"==":[{"var":"person.campusId"},{"__ctRef":true,"kind":"campus","key":"mz"}]}]}}},
 "process":{"a":{"handleMembership":{"groupTypeRoleId":{"__ctRef":true,"kind":"group-type-role","groupType":"group","role":"leiter"}}}}}`
	refs := func(role string) string {
		return `    "group-type:group" = "11"
    "group:other" = churchtools_group.other.id
    "campus:mz" = "0"
    "group-type-role:group/leiter" = "` + role + `"`
	}
	cfg := func(role string) string {
		return dgConfig(mock.URL, "501", "active", jsonLit(rs), refs(role)) + `
resource "churchtools_group" "other" {
  name            = "Other"
  group_type_id   = "11"
  group_status_id = "1"
}`
	}
	var otherID string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg("91"),
				Check: func(s *terraform.State) error {
					otherID = s.RootModule().Resources["churchtools_group.other"].Primary.ID
					live, _ := mock.Dynamic(501)
					b, _ := json.Marshal(live)
					want := `"groupTypeRoleId":91`
					if !regexp.MustCompile(want).Match(b) || regexp.MustCompile("__ctRef").Match(b) {
						t.Errorf("markers not resolved to integers: %s", b)
					}
					if !regexp.MustCompile(`\[`+otherID+`\]`).Match(b) || !regexp.MustCompile(`"person.campusId"\},0\]`).Match(b) {
						t.Errorf("ids missing: %s", b)
					}
					return nil
				},
			},
			{
				Config:             cfg("91"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
			{
				Config: cfg("92"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(dgAddr, plancheck.ResourceActionUpdate)}},
				Check: func(*terraform.State) error {
					live, _ := mock.Dynamic(501)
					b, _ := json.Marshal(live)
					if !regexp.MustCompile(`"groupTypeRoleId":92`).Match(b) {
						t.Errorf("changed ref not written: %s", b)
					}
					return nil
				},
			},
		},
	})
}

func TestAccDynamicGroup_ValidationErrors(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 502, 1)
	marker := `{"description":"d","query":{"x":{"__ctRef":true,"kind":"group","key":"k"}},"process":{}}`
	plain := `{"description":"d","query":{},"process":{}}`
	cases := []struct {
		name, cfg, re string
	}{
		{"marker without refs entry", dgConfig(mock.URL, "502", "active", jsonLit(marker), `    "campus:x" = "1"`), "Marker ohne refs-Eintrag"},
		{"marker and an unused entry", dgConfig(mock.URL, "502", "active", jsonLit(marker), `    "group:k" = "1"
    "group:unused" = "2"`), "refs-Eintrag ohne Marker"},
		{"unused refs entry", dgConfig(mock.URL, "502", "active", jsonLit(plain), `    "group:k" = "1"`), "refs-Eintrag ohne Marker"},
		{"status none", dgConfig(mock.URL, "502", "none", jsonLit(plain), ""), "Ungültiger Status"},
		{"bad json", dgConfig(mock.URL, "502", "active", `"{not json"`, ""), "kein gültiges JSON"},
		{"not an object", dgConfig(mock.URL, "502", "active", `"\"text\""`, ""), "falsche Form"},
		{"unknown marker kind", dgConfig(mock.URL, "502", "active",
			jsonLit(`{"query":{"x":{"__ctRef":true,"kind":"bogus","key":"k"}}}`), ""), "unbekanntem kind"},
		{"bad refs key", dgConfig(mock.URL, "502", "active", jsonLit(plain), `    "nonsense" = "1"`), "Ungültiger refs-Schlüssel"},
		{"non-numeric group id", dgConfig(mock.URL, "abc", "active", jsonLit(plain), ""), "keine numerische Gruppen-id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6(),
				Steps:                    []resource.TestStep{{Config: c.cfg, PlanOnly: true, ExpectError: regexp.MustCompile(c.re)}},
			})
		})
	}
	t.Run("non-numeric ref value", func(t *testing.T) {
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6(),
			Steps: []resource.TestStep{{
				Config:      dgConfig(mock.URL, "502", "active", jsonLit(marker), `    "group:k" = "abc"`),
				ExpectError: regexp.MustCompile("keine numerische ChurchTools-id"),
				PlanOnly:    true, // caught at validate time, not at apply
			}},
		})
	})
}

// Live holds cosmetic dterm labels, string ints, timestamps and the array envelope;
// the config holds the clean form. No PUT on create, and no diff afterwards. A later
// live change that is again only cosmetic stays a no-op; a real one is a diff.
func TestAccDynamicGroup_NoDiffOnCosmeticDifferences(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 503, 3) // archived: the endpoints must work for status 3
	live := `[{"description":"d","dynamicGroupUpdateStarted":"2026-01-01 00:00:00","dynamicGroupUpdateFinished":"2026-01-01 00:00:01",
	  "query":{"method":"ChurchQuery","params":{"filter":{"and":[
	    {"dterm":["Campus",{"==":[{"var":"ctgroup.campusId"},"1"]}]},
	    {"dterm":[{"title":"group.x.title"},{"oneof":[{"var":"ctgroup.id"},["5","6"]]}]},
	    {"!":[{"dterm":[{"stereotype":["groupmembership"],"title":"M"},{"oneof":[{"var":"ctgroup.id"},["42"]]}]}]}]}}},
	  "process":{"groupOnly":{"active":"none"}}}]`
	mock.SeedDynamic(503, "active", live)
	clean := `{"description":"d","query":{"method":"ChurchQuery","params":{"filter":{"and":[
	  {"==":[{"var":"ctgroup.campusId"},1]},
	  {"oneof":[{"var":"ctgroup.id"},[5,6]]},
	  {"!":[{"dterm":[{"stereotype":["groupmembership"],"title":"M"},{"oneof":[{"var":"ctgroup.id"},[42]]}]}]}]}}},
	  "process":{"groupOnly":{"active":"none"}}}`
	cfg := dgConfig(mock.URL, "503", "active", jsonLit(clean), "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: func(*terraform.State) error {
					if rs, _ := mock.DynamicPuts(503); rs != 0 {
						t.Errorf("ruleset PUT %d times although it was equal", rs)
					}
					return nil
				},
			},
			{Config: cfg, PlanOnly: true},
			{ResourceName: dgAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "group_id",
				ImportStateVerifyIgnore: []string{"ruleset"}},
			{
				PreConfig: func() { mock.SeedDynamic(503, "active", live) },
				Config:    cfg, PlanOnly: true,
			},
			{
				PreConfig: func() {
					mock.SeedDynamic(503, "active", `[{"description":"changed in CT","query":{},"process":{}}]`)
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// The import id is the group id; a name or a missing group is refused.
func TestAccDynamicGroup_ImportErrors(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config:        dgConfig(mock.URL, "9", "active", jsonLit(baseRuleset), ""),
				ResourceName:  dgAddr,
				ImportState:   true,
				ImportStateId: "not-a-number",
				ExpectError:   regexp.MustCompile("keine numerische Gruppen-id"),
			},
			{
				Config:        dgConfig(mock.URL, "9", "active", jsonLit(baseRuleset), ""),
				ResourceName:  dgAddr,
				ImportState:   true,
				ImportStateId: "9",
				ExpectError:   regexp.MustCompile("hat keine dynamische Regel"),
			},
		},
	})
}

// A plain group (no ruleset, 404) imports and plans the PUT instead of failing.
func TestAccDynamicGroup_PromotePlainGroup(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 504, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: dgConfig(mock.URL, "504", "active", jsonLit(baseRuleset), ""),
			Check: func(*terraform.State) error {
				if rs, st := mock.DynamicPuts(504); rs != 1 || st != 1 {
					t.Errorf("puts = %d/%d", rs, st)
				}
				return nil
			},
		}},
	})
}

// Import of a group whose live rule matches the markers + refs in the config: the plan
// shows a refs-only update (OpenTofu rejects a plan that drops a configured value for
// a null prior), but applying it writes nothing (no status PUT, no ruleset PUT).
func TestAccDynamicGroup_ImportWithRefsIsNoop(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 505, 1)
	mock.SeedDynamic(505, "active", `[{"description":"d","dynamicGroupUpdateStarted":"t",
	  "query":{"method":"ChurchQuery","params":{"filter":{"and":[
	    {"dterm":["lbl",{"==":[{"var":"ctgroup.groupTypeId"},"11"]}]},
	    {"oneof":[{"var":"ctgroup.id"},[42]]}]}}},"process":{}}]`)
	rs := `{"description":"d","query":{"method":"ChurchQuery","params":{"filter":{"and":[
	  {"==":[{"var":"ctgroup.groupTypeId"},{"__ctRef":true,"kind":"group-type","key":"group"}]},
	  {"oneof":[{"var":"ctgroup.id"},[{"__ctRef":true,"kind":"group","key":"other"}]]}]}}},"process":{}}`
	cfg := dgConfig(mock.URL, "505", "active", jsonLit(rs), `    "group-type:group" = "11"
    "group:other" = "42"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, ResourceName: dgAddr, ImportState: true, ImportStateId: "505", ImportStatePersist: true},
			{
				Config: cfg,
				Check: func(*terraform.State) error {
					if rs, st := mock.DynamicPuts(505); rs != 0 || st != 0 {
						t.Errorf("import + refs wrote: ruleset %d, status %d", rs, st)
					}
					return nil
				},
			},
		},
	})
}

// Create must not overwrite a different live rule; an equal one is adopted without a write.
func TestAccDynamicGroup_CreateGuardsExistingRule(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 506, 1)
	mock.SeedDynamic(506, "active", `[{"description":"other rule","query":{},"process":{}}]`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config:      dgConfig(mock.URL, "506", "active", jsonLit(baseRuleset), ""),
				ExpectError: regexp.MustCompile("hat bereits eine Regel"),
			},
			{
				Config: dgConfig(mock.URL, "506", "active", jsonLit(`{"description":"other rule","query":{},"process":{}}`), ""),
				Check: func(*terraform.State) error {
					if rs, st := mock.DynamicPuts(506); rs != 0 || st != 0 {
						t.Errorf("adopting an equal rule wrote: %d/%d", rs, st)
					}
					return nil
				},
			},
		},
	})
}

// `manual`: status first, then the ruleset.
func TestAccDynamicGroup_ManualWritesStatusFirst(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 507, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: dgConfig(mock.URL, "507", "manual", jsonLit(baseRuleset), ""),
			Check: func(*terraform.State) error {
				if ops := mock.DynamicOps(507); !reflect.DeepEqual(ops, []string{"status", "ruleset"}) {
					t.Errorf("manual: PUT order = %v", ops)
				}
				return nil
			},
		}},
	})
}

// A group without a rule is not imported (a wrong id must not promote a plain group).
func TestAccDynamicGroup_ImportRefusesPlainGroup(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedGroup(mock, 508, 1)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: dgConfig(mock.URL, "508", "active", jsonLit(baseRuleset), ""), ResourceName: dgAddr,
			ImportState: true, ImportStateId: "508", ExpectError: regexp.MustCompile("hat keine dynamische Regel"),
		}},
	})
}
