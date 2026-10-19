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
			// Destroy really deletes.
			if mock.RoleDeletes() != 1 {
				return fmt.Errorf("DELETE reached the role %d times, want 1", mock.RoleDeletes())
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
	if mock.RoleDeletes() != 1 { // only the final destroy
		t.Fatalf("role deleted %d times, want 1 (the final destroy)", mock.RoleDeletes())
	}
}

// Destroy deletes, and surfaces CT's 409 while memberships reference the role.
func TestAccGroupRoleDefinition_DestroyBlockedByMembers(t *testing.T) {
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
				PreConfig:   func() { mock.SetRoleMembers(id, 3) },
				Config:      providerBlock(mock.URL), // removed from config: the plan destroys it
				ExpectError: regexp.MustCompile(`(?s)Rolle kann nicht gelöscht werden.*409.*Mitgliedschaft.*3 Mitgliedschaft`),
			},
			{
				PreConfig: func() {
					if mock.RoleDeletes() != 0 || mock.Row("/group/roles", id) == nil {
						panic("the blocked destroy removed the role")
					}
					mock.SetRoleMembers(id, 0)
				},
				Config: providerBlock(mock.URL),
				Check: func(*terraform.State) error {
					if mock.Row("/group/roles", id) != nil || mock.RoleDeletes() != 1 {
						return fmt.Errorf("role not deleted (deletes %d)", mock.RoleDeletes())
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
