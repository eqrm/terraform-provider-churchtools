package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock acceptance tests for churchtools_grow_path (/person/growpaths).

func growPathCfg(mock *testmock.Server, name, color, extra string) string {
	return providerBlock(mock.URL) + fmt.Sprintf(`
resource "churchtools_grow_path" "x" {
  name  = %q
  color = %q
  %s
}
`, name, color, extra)
}

func TestAccGrowPath_CRUDAndImport(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	const addr = "churchtools_grow_path.x"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: growPathCfg(mock, "Friend", "sky", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", "Friend"),
					resource.TestCheckResourceAttr(addr, "color", "sky"),
					resource.TestCheckResourceAttr(addr, "sort_key", "0"),
				),
			},
			{Config: growPathCfg(mock, "Friend", "sky", ""), PlanOnly: true},
			{
				Config: growPathCfg(mock, "Friend of EQRM", "teal", "sort_key = 2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "Friend of EQRM"),
					resource.TestCheckResourceAttr(addr, "color", "teal"),
					resource.TestCheckResourceAttr(addr, "sort_key", "2"),
				),
			},
			{Config: growPathCfg(mock, "Friend of EQRM", "teal", "sort_key = 2"), PlanOnly: true},
			{Config: growPathCfg(mock, "Friend of EQRM", "teal", "sort_key = 2"), ResourceName: addr, ImportState: true, ImportStateVerify: true},
		},
		CheckDestroy: func(*terraform.State) error {
			if mock.GrowPathDeletes() != 0 {
				return fmt.Errorf("DELETE reached a growth path %d times, want 0 (Delete only un-manages)", mock.GrowPathDeletes())
			}
			return nil
		},
	})
}

// An omitted sortKey resets to 0 on a live PUT, so the provider always sends it:
// a rename with sort_key left out keeps the stored value.
func TestAccGrowPath_SortKeySurvivesUpdate(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/person/growpaths", 3, map[string]any{"name": "Fan", "color": "blue", "sortKey": float64(5)})
	imp := "\nimport {\n  to = churchtools_grow_path.x\n  id = \"3\"\n}\n"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: growPathCfg(mock, "Fan", "blue", "") + imp,
				Check:  resource.TestCheckResourceAttr("churchtools_grow_path.x", "sort_key", "5"),
			},
			{
				Config: growPathCfg(mock, "Fan 2", "blue", ""),
				Check: func(*terraform.State) error {
					put := mock.LastPut("/person/growpaths")
					if put["sortKey"] != float64(5) {
						return fmt.Errorf("PUT sortKey = %v, want 5; body %v", put["sortKey"], put)
					}
					if live := mock.Row("/person/growpaths", 3); live["sortKey"] != float64(5) || live["name"] != "Fan 2" {
						return fmt.Errorf("live row %v", live)
					}
					return nil
				},
			},
			{Config: growPathCfg(mock, "Fan 2", "blue", ""), PlanOnly: true},
		},
	})
}

// CT accepts the PUT but stores another name: the read-back turns that into an
// apply error instead of writing the plan into state.
func TestAccGrowPath_ReadBackCatchesDivergence(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: growPathCfg(mock, "Friend", "sky", "")},
			{
				PreConfig:   func() { mock.SetGrowPathPutHook(func(row map[string]any) { row["name"] = "Frien" }) },
				Config:      growPathCfg(mock, "Friend 2", "sky", ""),
				ExpectError: regexp.MustCompile(`nicht so gespeichert`),
			},
		},
	})
}

func TestAccGrowPath_GoneInCTLeavesState(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/person/growpaths", 3, map[string]any{"name": "Fan", "color": "blue", "sortKey": float64(1)})
	cfg := growPathCfg(mock, "Fan", "blue", "") + "\nimport {\n  to = churchtools_grow_path.x\n  id = \"3\"\n}\n"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			// 404 on GET of the removed id: the resource leaves state, so the plan wants to create it again.
			{PreConfig: func() { mock.Remove("/person/growpaths", 3) }, Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true},
		},
	})
}

func TestAccGrowPath_InvalidColor(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config:      growPathCfg(mock, "Friend", "hotpink", ""),
			ExpectError: regexp.MustCompile(`Ungültige Farbe`),
		}},
	})
}

// Destroy never calls DELETE (warning only); the mock's own DELETE contract is
// pinned here directly: 409 while a role references the stage, a dry run
// reports without deleting, 204 once free, and 204 for an unknown id.
func TestGrowPathMock_DeleteContract(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/person/growpaths", 3, map[string]any{"name": "Fan", "color": "blue", "sortKey": float64(1)})
	mock.Seed("/group/roles", 160, map[string]any{"groupTypeId": float64(9), "name": "x", "growPathId": float64(3)})
	del := func(path string) int {
		req, _ := http.NewRequest(http.MethodDelete, mock.URL+"/api/person/growpaths/"+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := del("3"); got != http.StatusConflict {
		t.Fatalf("DELETE referenced = %d, want 409", got)
	}
	mock.Seed("/group/roles", 160, map[string]any{"groupTypeId": float64(9), "name": "x", "growPathId": nil})
	if got := del("3?dryRun=true"); got != http.StatusConflict {
		t.Fatalf("dry run = %d, want 409", got)
	}
	if mock.GrowPathDeletes() != 0 || mock.Row("/person/growpaths", 3) == nil {
		t.Fatal("dry run deleted")
	}
	if got := del("99"); got != http.StatusNoContent {
		t.Fatalf("DELETE unknown = %d, want 204", got)
	}
	if got := del("3"); got != http.StatusNoContent || mock.GrowPathDeletes() != 1 {
		t.Fatalf("DELETE free = %d (deletes %d), want 204 and 1", got, mock.GrowPathDeletes())
	}
}
