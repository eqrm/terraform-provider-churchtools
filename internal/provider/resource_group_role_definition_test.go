package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock acceptance tests for churchtools_group_role_definition. The mock models
// the contract of /group/roles: POST and PUT need all eight writable fields,
// nameTranslated is not writable, DELETE answers 409 while members exist.

// A role as on prod: shorty and growPathId are set and not managed below.
func seedProdRole(mock *testmock.Server) {
	mock.Seed("/group/roles", 160, map[string]any{
		"groupTypeId": float64(9), "name": "participant", "nameTranslated": "Teilnehmer",
		"shorty": "Pa", "sortKey": float64(7), "type": "participant", "isDefault": true, "isHidden": true,
		"growPathId": float64(4), "isLeader": false,
	})
}

const roleDefConfig = `
resource "churchtools_group_role_definition" "x" {
  group_type_id = "9"
  name          = %q
  type          = "participant"
}
`

func TestAccGroupRoleDefinition_CreateUpdateNoop(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	cfg := func(name, extra string) string {
		return providerBlock(mock.URL) + fmt.Sprintf(`
resource "churchtools_group_role_definition" "x" {
  group_type_id = "9"
  name          = %q
  type          = "leader"
  %s
}
`, name, extra)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg("Kleingruppenleiter", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("churchtools_group_role_definition.x", "id"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "name", "Kleingruppenleiter"),
					// create defaults, as ct-cli's registry
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "shorty", "Kleingrupp"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "sort_key", "0"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "is_default", "false"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "is_hidden", "false"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "name_translated", "Kleingruppenleiter"),
				),
			},
			{Config: cfg("Kleingruppenleiter", ""), PlanOnly: true},
			{
				// Update in place: the id stays, no replacement.
				Config: cfg("Kleingruppenleitung", "sort_key = 3\n  is_hidden = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("churchtools_group_role_definition.x", plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "name", "Kleingruppenleitung"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "sort_key", "3"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "is_hidden", "true"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "shorty", "Kleingrupp"),
				),
			},
			{Config: cfg("Kleingruppenleitung", "sort_key = 3\n  is_hidden = true"), PlanOnly: true},
		},
		CheckDestroy: func(*terraform.State) error {
			// Destroy only un-manages: no DELETE, the role stays in CT.
			if mock.RoleDeletes() != 0 {
				return fmt.Errorf("DELETE reached the role %d times, want 0", mock.RoleDeletes())
			}
			return nil
		},
	})
}

// A role imported by id plans clean with a minimal config, and an update sends
// the FULL object: shorty, growPathId, sortKey, isDefault and isHidden that the
// configuration does not manage survive; nameTranslated is never sent.
func TestAccGroupRoleDefinition_ImportAndUpdateKeepsUnmanaged(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedProdRole(mock)
	cfg := func(name string) string {
		return providerBlock(mock.URL) + fmt.Sprintf(roleDefConfig, name)
	}
	imp := "\nimport {\n  to = churchtools_group_role_definition.x\n  id = \"160\"\n}\n"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg("participant") + imp,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "id", "160"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "shorty", "Pa"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "grow_path_id", "4"),
					resource.TestCheckResourceAttr("churchtools_group_role_definition.x", "name_translated", "Teilnehmer"),
				),
			},
			{Config: cfg("participant"), PlanOnly: true},
			{
				Config: cfg("Teilnehmende"),
				Check: func(*terraform.State) error {
					put := mock.LastPut("/group/roles")
					want := map[string]any{
						"groupTypeId": float64(9), "name": "Teilnehmende", "type": "participant",
						"shorty": "Pa", "sortKey": float64(7), "isDefault": true, "isHidden": true, "growPathId": float64(4),
					}
					for k, v := range want {
						if put[k] != v {
							return fmt.Errorf("PUT body %s = %v (%T), want %v; body %v", k, put[k], put[k], v, put)
						}
					}
					for _, k := range []string{"nameTranslated", "id", "isLeader"} {
						if _, ok := put[k]; ok {
							return fmt.Errorf("PUT body carries read-only %s", k)
						}
					}
					live := mock.Row("/group/roles", 160)
					if live["growPathId"] != float64(4) || live["shorty"] != "Pa" {
						return fmt.Errorf("unmanaged fields lost: %v", live)
					}
					return nil
				},
			},
			{Config: cfg("Teilnehmende"), PlanOnly: true},
		},
	})
}

// A null growPathId stays null through an update (PUT requires the key).
func TestAccGroupRoleDefinition_NullGrowPathSurvives(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	cfg := func(name string) string { return providerBlock(mock.URL) + fmt.Sprintf(roleDefConfig, name) }
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg("Gast")},
			{Config: cfg("Gäste"), Check: func(*terraform.State) error {
				put := mock.LastPut("/group/roles")
				if v, ok := put["growPathId"]; !ok || v != nil {
					return fmt.Errorf("growPathId in PUT = %v (present %v), want an explicit null", v, ok)
				}
				return nil
			}},
		},
	})
}

// group_type_id never forces a replacement: a change is a plan error, and
// nothing reaches CT.
func TestAccGroupRoleDefinition_GroupTypeChangeIsAnError(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	cfg := providerBlock(mock.URL) + fmt.Sprintf(roleDefConfig, "Gast")
	moved := providerBlock(mock.URL) + `
resource "churchtools_group_role_definition" "x" {
  group_type_id = "10"
  name          = "Gast"
  type          = "participant"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{Config: moved, PlanOnly: true, ExpectError: regexp.MustCompile(`nicht änderbar`)},
			{Config: cfg, PlanOnly: true},
		},
	})
	if mock.RoleDeletes() != 0 { // even the final destroy never deletes
		t.Fatalf("role deleted %d times, want 0", mock.RoleDeletes())
	}
}

// Destroy only drops the role from state: no DELETE is sent, the role stays in
// CT even while memberships reference it.
func TestAccGroupRoleDefinition_DestroyOnlyUnmanages(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	cfg := providerBlock(mock.URL) + fmt.Sprintf(roleDefConfig, "Gast")
	var id int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: func(s *terraform.State) error {
				_, err := fmt.Sscanf(s.RootModule().Resources["churchtools_group_role_definition.x"].Primary.ID, "%d", &id)
				return err
			}},
			{
				PreConfig: func() { mock.SetRoleMembers(id, 3) },
				Config:    providerBlock(mock.URL), // removed from config: the plan destroys it
				Check: func(*terraform.State) error {
					if mock.RoleDeletes() != 0 || mock.Row("/group/roles", id) == nil {
						return fmt.Errorf("destroy touched the role in CT (deletes %d)", mock.RoleDeletes())
					}
					return nil
				},
			},
		},
	})
}

// A role deleted in the UI drops out of state on refresh and is planned as a create.
func TestAccGroupRoleDefinition_GoneInUI(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedProdRole(mock)
	cfg := providerBlock(mock.URL) + fmt.Sprintf(roleDefConfig, "participant") +
		"\nimport {\n  to = churchtools_group_role_definition.x\n  id = \"160\"\n}\n"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{PreConfig: func() { mock.Remove("/group/roles", 160) }, Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

func TestAccGroupRoleDefinition_Validation(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	for name, c := range map[string]struct{ body, err string }{
		"bad type":    {`group_type_id = "9"` + "\n name = \"Gast\"\n type = \"admin\"", `Ungültiger Rollentyp`},
		"short name":  {`group_type_id = "9"` + "\n name = \"G\"\n type = \"leader\"", `Ungültiger Rollenname`},
		"long shorty": {`group_type_id = "9"` + "\n name = \"Gast\"\n type = \"leader\"\n shorty = \"12345678901\"", `Ungültiges Kürzel`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6(),
				Steps: []resource.TestStep{{
					Config:      providerBlock(mock.URL) + "resource \"churchtools_group_role_definition\" \"x\" {\n" + c.body + "\n}\n",
					ExpectError: regexp.MustCompile(c.err),
				}},
			})
		})
	}
}

// Attributes the configuration leaves out must not be re-sent from STATE: the
// live row may have changed since (UI edit, CT clearing is_default). The mock
// bumps sortKey after every GET, so the row the Update reads is newer than the
// state; the PUT must carry that live value, not the state's. (Terraform then
// reports the changed computed value as an inconsistent result -- which is the
// drift surfacing, instead of being silently reverted.)
func TestAccGroupRoleDefinition_UpdateKeepsLiveUnmanaged(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	var served []float64
	mock.SetRoleGetHook(func(row map[string]any) {
		n, _ := row["sortKey"].(float64)
		served = append(served, n)
		row["sortKey"] = n + 1
	})
	cfg := func(name string) string {
		return providerBlock(mock.URL) + fmt.Sprintf(roleDefConfig, name)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg("Mitglied")},
			{
				Config:      cfg("Mitglieder"),
				ExpectError: regexp.MustCompile(`(?i)inconsistent|unexpected new value`),
			},
			{
				PreConfig: func() {
					// The Update read the live row (value u), PUT it, and the read-back
					// served u again. A PUT from state would have sent an older value.
					if len(served) < 3 || served[len(served)-1] != served[len(served)-2] {
						t.Errorf("PUT did not carry the live sort_key; GETs served %v, PUT %v", served, mock.LastPut("/group/roles"))
					}
				},
				Config:   cfg("Mitglieder"),
				PlanOnly: true,
			},
		},
	})
}

// POST succeeded but the read-back failed: the role is in state (tainted), so a
// retry replaces it in state. Since Delete only un-manages, the first role stays
// in CT and must be removed by hand.
func TestAccGroupRoleDefinition_CreateReadBackFailureKeepsID(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	cfg := providerBlock(mock.URL) + fmt.Sprintf(roleDefConfig, "Mitglied")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				PreConfig:   mock.FailNextRoleGet,
				Config:      cfg,
				ExpectError: regexp.MustCompile(`nicht lesbar`),
			},
			{
				Config: cfg,
				Check: func(*terraform.State) error {
					if mock.Row("/group/roles", 1001) == nil {
						return fmt.Errorf("the tainted role was deleted in CT; Delete must only un-manage")
					}
					if mock.Row("/group/roles", 1002) == nil {
						return fmt.Errorf("no replacement role")
					}
					return nil
				},
			},
		},
	})
}
