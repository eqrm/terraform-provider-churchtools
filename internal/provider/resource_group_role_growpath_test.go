package provider_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// grow_path_id on churchtools_group_role: PATCH /groups/{id}/roles/{roleId},
// mock only (the mock's PATCH is an assumption, see testmock.patchGroupRole).
// The grants match seedSLL, so every step writes no permission rows.

const growPathRoleConfig = `
resource "churchtools_group_role" "mitglied" {
  group_id = "1748"
  role     = "Mitglied"
  %s

  grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
  grant {
    right    = "churchdb:view station"
    campuses = ["24"]
  }
}
`

func roleGrowPathCfg(mock *testmock.Server, attr string) string {
	return providerBlock(mock.URL) + fmt.Sprintf(growPathRoleConfig, attr)
}

func livePath(mock *testmock.Server, want *int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got := mock.GroupRoleGrowPath(1748, 16829)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			return fmt.Errorf("live growPathId %v, want %v", got, want)
		}
		return nil
	}
}

func patchBodies(mock *testmock.Server, want ...map[string]any) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got := mock.GroupRolePatches()
		if len(got) == 0 && len(want) == 0 {
			return nil
		}
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf("PATCH bodies %v, want %v", got, want)
		}
		return nil
	}
}

func intp(v int) *int { return &v }

// Left out: no PATCH ever, the live value is neither in the way nor overwritten,
// and a stage set "in the UI" between runs is no diff.
func TestAccGroupRole_GrowPathUnmanaged(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.SeedGroupRoleGrowPath(1748, 16829, intp(4))
	cfg := roleGrowPathCfg(mock, "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grow_path_id", "4"),
					patchBodies(mock),
					livePath(mock, intp(4)),
				),
			},
			{PreConfig: func() { mock.SeedGroupRoleGrowPath(1748, 16829, intp(5)) }, Config: cfg, PlanOnly: true},
			{
				Config: cfg,
				Check:  resource.ComposeAggregateTestCheckFunc(patchBodies(mock), livePath(mock, intp(5))),
			},
		},
	})
}

// Set: PATCH carries only growPathId; changes are patched, drift is corrected,
// and removing the attribute afterwards neither plans a change nor clears CT.
func TestAccGroupRole_GrowPathManaged(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: roleGrowPathCfg(mock, "grow_path_id = 7"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grow_path_id", "7"),
					patchBodies(mock, map[string]any{"growPathId": float64(7)}),
					livePath(mock, intp(7)),
				),
			},
			{Config: roleGrowPathCfg(mock, "grow_path_id = 7"), PlanOnly: true},
			{
				Config: roleGrowPathCfg(mock, "grow_path_id = 9"),
				Check: resource.ComposeAggregateTestCheckFunc(
					patchBodies(mock, map[string]any{"growPathId": float64(7)}, map[string]any{"growPathId": float64(9)}),
					livePath(mock, intp(9)),
				),
			},
			{
				PreConfig:          func() { mock.SeedGroupRoleGrowPath(1748, 16829, intp(3)) },
				Config:             roleGrowPathCfg(mock, "grow_path_id = 9"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: roleGrowPathCfg(mock, "grow_path_id = 9"),
				Check:  livePath(mock, intp(9)),
			},
			{
				Config: roleGrowPathCfg(mock, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grow_path_id", "9"),
					livePath(mock, intp(9)),
				),
			},
			{Config: roleGrowPathCfg(mock, ""), PlanOnly: true},
		},
	})
}

// Left out, with a UI edit between refresh and apply: a grant-only update must
// not PATCH the stale state value back over it.
func TestAccGroupRole_GrowPathUnmanagedKeepsUIEditDuringApply(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.SeedGroupRoleGrowPath(1748, 16829, intp(4))
	withExtraGrant := providerBlock(mock.URL) + `
resource "churchtools_group_role" "mitglied" {
  group_id = "1748"
  role     = "Mitglied"

  grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
  grant {
    right    = "churchdb:view station"
    campuses = ["24"]
  }
  grant {
    right = "churchcal:view"
  }
}
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: roleGrowPathCfg(mock, ""), Check: livePath(mock, intp(4))},
			{
				PreConfig: func() { mock.SetGroupRoleGrowPathOnNextGrantWrite(1748, 16829, intp(5)) },
				Config:    withExtraGrant,
				Check:     resource.ComposeAggregateTestCheckFunc(patchBodies(mock), livePath(mock, intp(5))),
			},
		},
	})
}
