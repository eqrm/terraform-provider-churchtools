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

// clear_grow_path: the explicit "no stage" for both resources. Mock only: the
// OpenAPI types growPathId as integer|null on PUT /group/roles/{id} and
// PATCH /groups/{id}/roles/{roleId}; the null write has not run live.

// A stage set on the type-level role is cleared by clear_grow_path = true with a
// PUT that carries growPathId: null (and the other fields unchanged); the plan is
// then clean, a stage set in the UI later is corrected, and dropping the switch
// leaves the role unmanaged again.
func TestAccGroupRoleDefinition_ClearGrowPath(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedProdRole(mock)
	const addr = "churchtools_group_role_definition.x"
	imp := "\nimport {\n  to = churchtools_group_role_definition.x\n  id = \"160\"\n}\n"
	cfg := func(extra string) string {
		return providerBlock(mock.URL) + fmt.Sprintf(`
resource "churchtools_group_role_definition" "x" {
  group_type_id = "9"
  name          = "participant"
  type          = "participant"
  %s
}
`, extra)
	}
	putNull := func(*terraform.State) error {
		body := mock.LastPut("/group/roles")
		if v, ok := body["growPathId"]; !ok || v != nil {
			return fmt.Errorf("PUT growPathId = %v (present %v), want null", v, ok)
		}
		if body["shorty"] != "Pa" || body["sortKey"] != float64(7) {
			return fmt.Errorf("PUT changed other fields: %v", body)
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg("") + imp, Check: resource.TestCheckResourceAttr(addr, "grow_path_id", "4")},
			{
				Config: cfg("clear_grow_path = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "grow_path_id"),
					putNull,
				),
			},
			{Config: cfg("clear_grow_path = true"), PlanOnly: true},
			{
				PreConfig: func() {
					mock.Seed("/group/roles", 160, map[string]any{
						"groupTypeId": float64(9), "name": "participant", "nameTranslated": "participant",
						"shorty": "Pa", "sortKey": float64(7), "type": "participant", "isDefault": true, "isHidden": true,
						"growPathId": float64(5), "isLeader": false,
					})
				},
				Config:             cfg("clear_grow_path = true"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg("clear_grow_path = true"), Check: putNull},
			{Config: cfg("")},
			{Config: cfg(""), PlanOnly: true},
		},
	})
}

// The same on one group's role: PATCH carries only growPathId: null.
func TestAccGroupRole_ClearGrowPath(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.SeedGroupRoleGrowPath(1748, 16829, intp(4))
	const addr = "churchtools_group_role.mitglied"
	clearOnly := func(n int) resource.TestCheckFunc {
		return func(*terraform.State) error {
			got := mock.GroupRolePatches()
			if len(got) != n {
				return fmt.Errorf("%d PATCH calls %v, want %d", len(got), got, n)
			}
			if last := got[n-1]; len(last) != 1 || last["growPathId"] != nil {
				return fmt.Errorf("last PATCH body %v, want only growPathId: null", last)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: roleGrowPathCfg(mock, ""), Check: resource.TestCheckResourceAttr(addr, "grow_path_id", "4")},
			{
				Config: roleGrowPathCfg(mock, "clear_grow_path = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "grow_path_id"),
					livePath(mock, nil),
					clearOnly(1),
				),
			},
			{Config: roleGrowPathCfg(mock, "clear_grow_path = true"), PlanOnly: true},
			{
				PreConfig:          func() { mock.SeedGroupRoleGrowPath(1748, 16829, intp(6)) },
				Config:             roleGrowPathCfg(mock, "clear_grow_path = true"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: roleGrowPathCfg(mock, "clear_grow_path = true"),
				Check:  resource.ComposeAggregateTestCheckFunc(livePath(mock, nil), clearOnly(2)),
			},
			{Config: roleGrowPathCfg(mock, ""), Check: clearOnly(2)},
			{Config: roleGrowPathCfg(mock, ""), PlanOnly: true},
		},
	})
}

// A value and the clear switch together are refused at plan time, on both resources.
func TestAccGrowPathClear_ConflictsWithValue(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config:      roleGrowPathCfg(mock, "clear_grow_path = true\n  grow_path_id = 3"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`widersprechen sich`),
			},
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_group_role_definition" "x" {
  group_type_id   = "9"
  name            = "participant"
  type            = "participant"
  grow_path_id    = 3
  clear_grow_path = true
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`widersprechen sich`),
			},
		},
	})
}
