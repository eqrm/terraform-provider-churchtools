package provider_test

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func groupConfig(host, name, campusLine string) string {
	return providerBlock(host) + `
resource "churchtools_group" "g" {
  name            = "` + name + `"
  group_type_id   = "11"
  group_status_id = "2"
` + campusLine + `
}`
}

// The whole write lifecycle in one run: create, import, a name-only PATCH, campus
// "0" (a real campus, must not read as unset), clearing the campus (an explicit
// null on the wire and null in state), and a destroy that leaves the row.
func TestAccGroup_Lifecycle(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	const addr = "churchtools_group.g"

	patchIs := func(want map[string]any) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := mock.LastPatch("/groups"); !reflect.DeepEqual(got, want) {
				t.Errorf("PATCH body = %v, want %v", got, want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: groupConfig(mock.URL, "Gruppe 001", `  campus_id       = "1"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", "Gruppe 001"),
					resource.TestCheckResourceAttr(addr, "group_type_id", "11"),
					resource.TestCheckResourceAttr(addr, "group_status_id", "2"),
					resource.TestCheckResourceAttr(addr, "campus_id", "1"),
					func(*terraform.State) error {
						// Ints on the wire, as CT's POST takes them.
						row := mock.Find("/groups", "name", "Gruppe 001")
						if row == nil {
							t.Fatal("created group not in mock")
						}
						want := map[string]any{"groupTypeId": float64(11), "groupStatusId": float64(2), "campusId": float64(1)}
						if info := row["information"].(map[string]any); !reflect.DeepEqual(info, want) {
							t.Errorf("stored information = %v, want %v", info, want)
						}
						return nil
					},
				),
			},
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
			{
				Config: groupConfig(mock.URL, "Gruppe 001 neu", `  campus_id       = "1"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "Gruppe 001 neu"),
					patchIs(map[string]any{"name": "Gruppe 001 neu"}),
				),
			},
			{
				Config: groupConfig(mock.URL, "Gruppe 001 neu", `  campus_id       = "0"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "campus_id", "0"),
					patchIs(map[string]any{"campusId": float64(0)}),
				),
			},
			{
				Config: groupConfig(mock.URL, "Gruppe 001 neu", ``),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "campus_id"),
					patchIs(map[string]any{"campusId": nil}),
				),
			},
			{
				// Still empty on re-plan: null campus in CT equals an omitted attribute.
				Config:   groupConfig(mock.URL, "Gruppe 001 neu", ``),
				PlanOnly: true,
			},
		},
	})

	// The framework destroyed the resource at the end; the group must live on.
	if mock.Find("/groups", "name", "Gruppe 001 neu") == nil {
		t.Error("destroy removed the group from ChurchTools; Delete must only un-manage")
	}
}

// A group deleted in the UI is dropped from state by Read, so the next plan
// recreates it instead of erroring.
func TestAccGroup_DeletedOutOfBandIsRecreated(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	cfg := groupConfig(mock.URL, "Gruppe 002", ``)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					row := mock.Find("/groups", "name", "Gruppe 002")
					mock.Remove("/groups", int(row["id"].(float64)))
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg}, // apply recreates
		},
	})
}

// CT answering 200 and keeping the old value must fail the apply, not be stored.
func TestAccGroup_IgnoredPatchFieldFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/groups", 5001, map[string]any{
		"name": "Gruppe 003",
		"information": map[string]any{
			"groupTypeId": float64(11), "groupStatusId": float64(2), "campusId": nil,
		},
	})
	mock.IgnoreOnPatch("/groups", 5001, "campusId")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(mock.URL) + `
import {
  to = churchtools_group.g
  id = "5001"
}
resource "churchtools_group" "g" {
  name            = "Gruppe 003"
  group_type_id   = "11"
  group_status_id = "2"
}`,
			},
			{
				Config:      groupConfig(mock.URL, "Gruppe 003", `  campus_id       = "1"`),
				ExpectError: regexp.MustCompile(`(?s)ChurchTools hat die.*ignoriert`),
			},
		},
	})
}

func TestAccGroup_CreateWithoutIDFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.DropCreateID("/groups")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config:      groupConfig(mock.URL, "Gruppe 004", ``),
				ExpectError: regexp.MustCompile(`(?s)keine\s+id`),
			},
		},
	})
}

func TestAccGroup_ImportRejectsNonNumericID(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config:        groupConfig(mock.URL, "Gruppe 005", ``),
				ResourceName:  "churchtools_group.g",
				ImportState:   true,
				ImportStateId: "Gruppe 005",
				ExpectError:   regexp.MustCompile(`(?s)keine\s+numerische`),
			},
		},
	})
}

// seedTypeMigration installs group 5001 on type 11 with the roles Leiter (101),
// Mitglied (102, default) and Gast (103), plus the given roles on type 12. Memberships:
// two Leiter, one Mitglied, nobody Gast.
func seedTypeMigration(mock *testmock.Server, targets map[int]map[string]any) {
	mock.Seed("/groups", 5001, map[string]any{
		"name": "Gruppe 010",
		"information": map[string]any{
			"groupTypeId": float64(11), "groupStatusId": float64(2), "campusId": nil,
		},
	})
	role := func(id, typ int, name, kind string, def bool) {
		mock.Seed("/group/roles", id, map[string]any{"name": name, "groupTypeId": float64(typ), "type": kind, "isDefault": def})
	}
	role(101, 11, "Leiter", "leader", false)
	role(102, 11, "Mitglied", "participant", true)
	role(103, 11, "Gast", "participant", false)
	for id, r := range targets {
		role(id, 12, r["name"].(string), r["type"].(string), r["default"].(bool))
	}
	mock.SeedMembers(5001, 101, 101, 102)
}

func groupTypeConfig(host, name, typeID string) string {
	return providerBlock(host) + `
import {
  to = churchtools_group.g
  id = "5001"
}
resource "churchtools_group" "g" {
  name            = "` + name + `"
  group_type_id   = "` + typeID + `"
  group_status_id = "2"
}`
}

// A type change goes through POST /groups/{id}/grouptype with a mapping for every role
// of the old type, and PATCH never carries groupTypeId (CT 400s it). A rename in the
// same apply still travels by PATCH.
func TestAccGroup_TypeMigration(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeMigration(mock, map[int]map[string]any{
		201: {"name": "Leiter", "type": "leader", "default": false},
		202: {"name": "mitglied", "type": "participant", "default": true},
		203: {"name": "GAST", "type": "participant", "default": false},
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: groupTypeConfig(mock.URL, "Gruppe 010", "11")},
			{
				Config: groupTypeConfig(mock.URL, "Gruppe 010 neu", "12"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group.g", "group_type_id", "12"),
					func(*terraform.State) error {
						want := map[string]any{
							"groupTypeId": float64(12),
							"roleMapping": map[string]any{"101": float64(201), "102": float64(202), "103": float64(203)},
						}
						if got := mock.LastGroupTypePost(5001); !reflect.DeepEqual(got, want) {
							t.Errorf("POST grouptype body = %v, want %v", got, want)
						}
						if got, want := mock.LastPatch("/groups"), map[string]any{"name": "Gruppe 010 neu"}; !reflect.DeepEqual(got, want) {
							t.Errorf("PATCH body = %v, want %v", got, want)
						}
						if got, want := mock.Members(5001), []int{201, 201, 202}; !reflect.DeepEqual(got, want) {
							t.Errorf("members after migration = %v, want %v", got, want)
						}
						return nil
					},
				),
			},
		},
	})
}

// A role nobody holds and no same-named target for goes to the target type's default
// role, and a type-only change sends no PATCH at all.
func TestAccGroup_TypeMigrationEmptyRoleFallsBackToDefault(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeMigration(mock, map[int]map[string]any{
		201: {"name": "Leiter", "type": "leader", "default": false},
		202: {"name": "Mitglied", "type": "participant", "default": false},
		203: {"name": "Teilnehmer", "type": "participant", "default": true},
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: groupTypeConfig(mock.URL, "Gruppe 010", "11")},
			{
				Config: groupTypeConfig(mock.URL, "Gruppe 010", "12"),
				Check: func(*terraform.State) error {
					want := map[string]any{
						"groupTypeId": float64(12),
						"roleMapping": map[string]any{"101": float64(201), "102": float64(202), "103": float64(203)},
					}
					if got := mock.LastGroupTypePost(5001); !reflect.DeepEqual(got, want) {
						t.Errorf("POST grouptype body = %v, want %v", got, want)
					}
					if got := mock.LastPatch("/groups"); got != nil {
						t.Errorf("PATCH sent %v, want none for a type-only change", got)
					}
					return nil
				},
			},
		},
	})
}

// A role that holds members and has no same-named target stops the plan, before any write.
func TestAccGroup_TypeMigrationRefusesOccupiedUnmappedRole(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeMigration(mock, map[int]map[string]any{
		201: {"name": "Leiter", "type": "leader", "default": false},
		202: {"name": "Teilnehmer", "type": "participant", "default": true},
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: groupTypeConfig(mock.URL, "Gruppe 010", "11")},
			{
				Config:      groupTypeConfig(mock.URL, "Gruppe 010", "12"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)Gruppentyp kann nicht gewechselt.*"Mitglied" \(Rolle 102, 1 Mitglied`),
			},
			{
				Config:      groupTypeConfig(mock.URL, "Gruppe 010", "12"),
				ExpectError: regexp.MustCompile(`(?s)Gruppentyp kann nicht gewechselt`),
			},
			{
				PreConfig: func() {
					if got := mock.LastGroupTypePost(5001); got != nil {
						t.Errorf("a refused migration still POSTed %v", got)
					}
				},
				Config:   groupTypeConfig(mock.URL, "Gruppe 010", "11"),
				PlanOnly: true,
			},
		},
	})
}

// The only holders of Mitglied sit on member page 2 (the client pages at 100): a
// first-page-only read would call the role empty and map its members away.
func TestAccGroup_TypeMigrationCountsMembersOnLaterPages(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeMigration(mock, map[int]map[string]any{
		201: {"name": "Leiter", "type": "leader", "default": false},
		202: {"name": "Teilnehmer", "type": "participant", "default": true},
	})
	roles := make([]int, 0, 150)
	for i := 0; i < 100; i++ {
		roles = append(roles, 101)
	}
	for i := 0; i < 50; i++ {
		roles = append(roles, 102)
	}
	mock.SeedMembers(5001, roles...)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: groupTypeConfig(mock.URL, "Gruppe 010", "11")},
			{
				Config:      groupTypeConfig(mock.URL, "Gruppe 010", "12"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)"Mitglied" \(Rolle 102, 50 Mitglied`),
			},
		},
	})
}

// The migration lands, then CT ignores the rename in the same apply: the verify loop
// errors, but state must hold the new type (not the stale one), so the next plan
// shows only the ignored field.
func TestAccGroup_TypeMigrationKeepsNewTypeWhenVerifyFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeMigration(mock, map[int]map[string]any{
		201: {"name": "Leiter", "type": "leader", "default": false},
		202: {"name": "Mitglied", "type": "participant", "default": true},
		203: {"name": "Gast", "type": "participant", "default": false},
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: groupTypeConfig(mock.URL, "Gruppe 010", "11")},
			{
				PreConfig:   func() { mock.IgnoreOnPatch("/groups", 5001, "name") },
				Config:      groupTypeConfig(mock.URL, "Gruppe 010 neu", "12"),
				ExpectError: regexp.MustCompile(`ChurchTools hat die Änderung ignoriert`),
			},
			{
				// Config on the old name and the new type: a no-op only if state recorded type 12.
				Config:   groupTypeConfig(mock.URL, "Gruppe 010", "12"),
				PlanOnly: true,
			},
		},
	})
}

// inform_leader is Optional + Computed. Unset: never sent (create or update), no
// diff, the value CT holds is read into state. Set: sent, read back, enforced.
// The mock's default for a new group is set to true here, so "unset" reads back
// true only if the provider sent nothing.
func TestAccGroup_InformLeader(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.GroupInformLeaderDefault(true)
	const addr = "churchtools_group.g"

	sent := func(want map[string]any) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := mock.LastPatch("/groups"); !reflect.DeepEqual(got, want) {
				t.Errorf("PATCH body = %v, want %v", got, want)
			}
			return nil
		}
	}
	held := func(want bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			row := mock.Find("/groups", "name", "Gruppe IL")
			if row == nil {
				t.Fatal("group not in mock")
			}
			if got := row["settings"].(map[string]any)["informLeader"]; got != want {
				t.Errorf("CT holds informLeader = %v, want %v", got, want)
			}
			return nil
		}
	}
	cfg := func(line string) string { return groupConfig(mock.URL, "Gruppe IL", line) }

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				// Unset on create: not in the POST body, state takes CT's value.
				Config: cfg(``),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "inform_leader", "true"),
					func(*terraform.State) error {
						if _, has := mock.LastPost("/groups")["informLeader"]; has {
							t.Error("unset inform_leader was sent on create")
						}
						return nil
					},
				),
			},
			{Config: cfg(``), PlanOnly: true}, // no diff, no "(known after apply)"
			{
				// Set to false: PATCH carries only it, CT holds it, state reads it back.
				Config: cfg(`  inform_leader = false`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "inform_leader", "false"),
					sent(map[string]any{"informLeader": false}), held(false),
				),
			},
			{Config: cfg(`  inform_leader = false`), PlanOnly: true},
			{
				// Back to unset: the provider lets go, the value stays and nothing is sent.
				PreConfig: func() { mock.ClearLastPatch("/groups") },
				Config:    cfg(``),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "inform_leader", "false"),
					sent(nil), held(false),
				),
			},
			{Config: cfg(``), PlanOnly: true},
			{
				Config: cfg(`  inform_leader = true`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "inform_leader", "true"),
					sent(map[string]any{"informLeader": true}), held(true),
				),
			},
			{
				// Drift: someone flips it in the UI. Set in config, so it is detected and put back.
				PreConfig: func() {
					row := mock.Find("/groups", "name", "Gruppe IL")
					row["settings"].(map[string]any)["informLeader"] = false
				},
				Config:             cfg(`  inform_leader = true`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg(`  inform_leader = true`),
				Check:  resource.ComposeTestCheckFunc(held(true), sent(map[string]any{"informLeader": true})),
			},
			{
				// Unset: drift is read into state but is no diff.
				PreConfig: func() {
					row := mock.Find("/groups", "name", "Gruppe IL")
					row["settings"].(map[string]any)["informLeader"] = false
				},
				Config:   cfg(``),
				PlanOnly: true,
			},
		},
	})
}

// Set on create: live CT ignores informLeader on POST, so it is not sent there
// but PATCHed right after, and read back. A value CT already holds is not PATCHed.
func TestAccGroup_InformLeaderOnCreate(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	notInPost := func(*terraform.State) error {
		if _, has := mock.LastPost("/groups")["informLeader"]; has {
			t.Error("informLeader was sent on POST, which live CT ignores")
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: groupConfig(mock.URL, "Gruppe IL2", `  inform_leader = true`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group.g", "inform_leader", "true"),
					notInPost,
					func(*terraform.State) error {
						if got := mock.LastPatch("/groups"); !reflect.DeepEqual(got, map[string]any{"informLeader": true}) {
							t.Errorf("PATCH body = %v, want only informLeader: true", got)
						}
						row := mock.Find("/groups", "name", "Gruppe IL2")
						if got := row["settings"].(map[string]any)["informLeader"]; got != true {
							t.Errorf("CT holds informLeader = %v, want true", got)
						}
						return nil
					},
				),
			},
			{Config: groupConfig(mock.URL, "Gruppe IL2", `  inform_leader = true`), PlanOnly: true},
		},
	})

	mock2 := testmock.New()
	defer mock2.Close()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: groupConfig(mock2.URL, "Gruppe IL2b", `  inform_leader = false`),
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("churchtools_group.g", "inform_leader", "false"),
				func(*terraform.State) error {
					if got := mock2.LastPatch("/groups"); got != nil {
						t.Errorf("PATCH %v sent although CT already holds false", got)
					}
					return nil
				},
			),
		}},
	})
}

// CT ignoring the PATCH that sets inform_leader on create fails the apply and
// keeps the group in state on what CT holds.
func TestAccGroup_InformLeaderOnCreateIgnoredFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.IgnoreOnPatch("/groups", 1001, "informLeader") // the mock's first create id
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config:      groupConfig(mock.URL, "Gruppe IL4", `  inform_leader = true`),
			ExpectError: regexp.MustCompile(`(?s)ChurchTools hat die.*ignoriert`),
		}},
	})
}

// CT accepting inform_leader and keeping the old value fails the apply.
func TestAccGroup_InformLeaderIgnoredFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/groups", 5002, map[string]any{
		"name":        "Gruppe IL3",
		"information": map[string]any{"groupTypeId": float64(11), "groupStatusId": float64(2), "campusId": nil},
		"settings":    map[string]any{"informLeader": true},
	})
	mock.IgnoreOnPatch("/groups", 5002, "informLeader")
	imp := providerBlock(mock.URL) + `
import {
  to = churchtools_group.g
  id = "5002"
}
resource "churchtools_group" "g" {
  name            = "Gruppe IL3"
  group_type_id   = "11"
  group_status_id = "2"
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: imp + "}"},
			{Config: imp + "  inform_leader = false\n}", ExpectError: regexp.MustCompile(`(?s)ChurchTools hat die.*ignoriert`)},
		},
	})
}
