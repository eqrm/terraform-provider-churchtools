package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock acceptance tests for churchtools_person_status_grants (permission
// domain `status`, addressed by the status id).

func seedStatus(mock *testmock.Server, withLogin bool) {
	mock.Seed("/statuses", 0, map[string]any{"name": "Unbekannt", "shorty": "UN", "isActive": true, "sortKey": float64(0)})
	var rows []testmock.PermRow
	for _, id := range []int64{1, 2, 3, 4, 5, 6, 7, 8} { // CT's baseline, never touched
		rows = append(rows, testmock.PermRow{AuthID: 100 + id, Type: "grant", ModifiedPid: -1})
	}
	if withLogin {
		rows = append(rows, testmock.PermRow{AuthID: 18, DataID: i64p(-1), Type: "grant", ModifiedPid: 1})
	}
	rows = append(rows, testmock.PermRow{AuthID: 15, Type: "grant", ModifiedPid: 1})
	mock.SeedDomainPermissions("status", 0, rows)
}

const statusLogin = `grant {
    right    = "churchcore:login to external system"
    data_ids = ["-1"]
  }`

func statusCfg(url, grants, extra string) string {
	return providerBlock(url) + fmt.Sprintf(`
resource "churchtools_person_status_grants" "x" {
  person_status_id = "0"
  %s
  %s
}
`, extra, grants)
}

func statusHas(mock *testmock.Server, auth int64, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got := false
		for _, r := range mock.DomainPermissions("status", 0) {
			if r.AuthID == auth {
				got = true
			}
		}
		if got != want {
			return fmt.Errorf("authId %d present=%v, want %v; rows %+v", auth, got, want, mock.DomainPermissions("status", 0))
		}
		return nil
	}
}

func TestAccPersonStatusGrants_Lifecycle(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedStatus(mock, true)
	login := statusLogin + "\n  grant {\n    right = \"churchcore:administer custom modules\"\n  }"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				// Adopts the live rows; the baseline is not in state.
				Config: statusCfg(mock.URL, login, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_person_status_grants.x", "id", "0"),
					resource.TestCheckResourceAttr("churchtools_person_status_grants.x", "grant.#", "2"),
					statusHas(mock, 18, true), statusHas(mock, 15, true), statusHas(mock, 101, true),
				),
			},
			{Config: statusCfg(mock.URL, login, ""), PlanOnly: true},
			{
				// Dropping a grant revokes exactly that row, by dataId [-1].
				Config: statusCfg(mock.URL, "grant {\n    right = \"churchcore:administer custom modules\"\n  }", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					statusHas(mock, 18, false), statusHas(mock, 15, true), statusHas(mock, 101, true),
					func(*terraform.State) error {
						for _, w := range mock.PermissionWrites() {
							if w == "DELETE 18 [-1]" {
								return nil
							}
						}
						return fmt.Errorf("no exact DELETE 18 [-1] in %v", mock.PermissionWrites())
					},
				),
			},
			{Config: statusCfg(mock.URL, "grant {\n    right = \"churchcore:administer custom modules\"\n  }", ""), PlanOnly: true},
			{
				// Granting again writes the row back.
				Config: statusCfg(mock.URL, login+"\n  grant {\n    right = \"churchcore:administer custom modules\"\n  }", ""),
				Check:  statusHas(mock, 18, true),
			},
		},
	})
}

func TestAccPersonStatusGrants_Import(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedStatus(mock, true)
	cfg := statusCfg(mock.URL, statusLogin+"\n  grant {\n    right = \"churchcore:administer custom modules\"\n  }", "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{ResourceName: "churchtools_person_status_grants.x", ImportState: true, ImportStateId: "0",
				ImportStateVerify: true, Config: cfg},
		},
	})
}

func TestAccPersonStatusGrants_PreserveUnknown(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedStatus(mock, true)
	cfg := statusCfg(mock.URL, "grant {\n    right = \"churchcore:administer custom modules\"\n  }", `preserve_unknown = ["oauth_client"]`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.ComposeAggregateTestCheckFunc(
				// the login row is kept out of state, so it is never revoked
				statusHas(mock, 18, true), statusHas(mock, 15, true),
				resource.TestCheckResourceAttr("churchtools_person_status_grants.x", "grant.#", "1"),
			)},
			{Config: cfg, PlanOnly: true},
		},
	})
}

func TestAccPersonStatusGrants_Errors(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedStatus(mock, false)
	t.Run("authId above 10000", func(t *testing.T) {
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6(),
			Steps: []resource.TestStep{{
				Config:      statusCfg(mock.URL, "grant {\n    right = \"churchdb:+see persons\"\n  }", ""),
				ExpectError: regexp.MustCompile(`(?s)Personenstatus|10000`),
			}},
		})
	})
	t.Run("no subgroups argument", func(t *testing.T) {
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6(),
			Steps: []resource.TestStep{{
				Config:      statusCfg(mock.URL, "grant {\n    right = \"churchcore:administer custom modules\"\n    applies_to_subgroups = true\n  }", ""),
				ExpectError: regexp.MustCompile(`applies_to_subgroups`),
			}},
		})
	})
	t.Run("status missing", func(t *testing.T) {
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6(),
			Steps: []resource.TestStep{{
				Config:      providerBlock(mock.URL) + "resource \"churchtools_person_status_grants\" \"x\" {\n person_status_id = \"77\"\n grant {\n right = \"churchcore:administer custom modules\"\n }\n}\n",
				ExpectError: regexp.MustCompile(`existiert nicht`),
			}},
		})
	})
}
