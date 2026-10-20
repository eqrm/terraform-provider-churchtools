package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Mock acceptance tests for churchtools_group_type_role (permission domain
// group_type_role, addressed by the role id). The mock refuses type=revoke on
// this domain like CT does, so a provider that tried one would fail here.

const typeRoleID = 160

func seedTypeRole(mock *testmock.Server) {
	mock.Seed("/group/roles", typeRoleID, map[string]any{
		"groupTypeId": float64(9), "name": "participant", "nameTranslated": "Teilnehmer",
		"shorty": "Pa", "sortKey": float64(1), "type": "participant", "isDefault": true, "isHidden": false,
		"growPathId": nil, "isLeader": false,
	})
	mock.SeedDomainPermissions("group_type_role", typeRoleID, []testmock.PermRow{
		{AuthID: 102, DataID: i64p(2), Type: "grant", ModifiedPid: 1},
		{AuthID: 124, DataID: i64p(24), Type: "grant", ModifiedPid: 1},
		{AuthID: 10101, DataID: i64p(3), IsInherited: true, Type: "grant", ModifiedPid: 1},
		// CT's own baseline row: never declared, never revoked.
		{AuthID: 1101, Type: "grant", ModifiedPid: -1},
	})
}

const typeRoleConfig = `
resource "churchtools_group_type_role" "participant" {
  role_id          = "160"
  preserve_unknown = ["crp_query"]

  grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
  grant {
    right    = "churchdb:view station"
    campuses = ["24"]
  }
  grant {
    right                = "churchdb:+see persons"
    applies_to_subgroups = true
    security_levels      = ["3"]
  }
  %s
}
`

func typeRows(mock *testmock.Server) []testmock.PermRow {
	return mock.DomainPermissions("group_type_role", typeRoleID)
}

func typeHas(mock *testmock.Server, auth int64, data *int64, inherited, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got := false
		for _, r := range typeRows(mock) {
			if r.AuthID == auth && r.IsInherited == inherited && ((data == nil && r.DataID == nil) || (data != nil && r.DataID != nil && *r.DataID == *data)) {
				got = true
			}
		}
		if got != want {
			return fmt.Errorf("row %d %v inherited=%v present=%v, want %v; rows %+v", auth, data, inherited, got, want, typeRows(mock))
		}
		return nil
	}
}

// Create adopts the live rows, no-op plans stay empty, an update revokes by
// exact id (DELETE, never a deny row), and the inherited row is its own grant.
func TestAccGroupTypeRole_Lifecycle(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeRole(mock)
	cfg := providerBlock(mock.URL) + fmt.Sprintf(typeRoleConfig, queryGrant)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "id", "160"),
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "group_type_id", "9"),
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "role", "participant"),
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "grant.#", "4"),
					// Only the missing row is written; the other three were live.
					lastWrites(mock, "PUT 702 [3]"),
				),
			},
			{Config: cfg, PlanOnly: true},
			{Config: cfg, PlanOnly: true},
			{
				// Drop the campus grant and the inherited grant: exact revokes,
				// the inherited one addressed with its isInherited flag.
				Config: providerBlock(mock.URL) + `
resource "churchtools_group_type_role" "participant" {
  role_id          = "160"
  preserve_unknown = ["crp_query"]

  grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					lastWrites(mock, "DELETE 124 [24]", "DELETE 702 [3]", "DELETE 10101 inherited [3]"),
					typeHas(mock, 124, i64p(24), false, false),
					typeHas(mock, 10101, i64p(3), true, false),
					typeHas(mock, 102, i64p(2), false, true),
					// 702 [3] was declared before, so undeclaring it revokes it, even
					// on a preserved dimension; the system baseline is untouched.
					typeHas(mock, 702, i64p(3), false, false),
					typeHas(mock, 1101, nil, false, true),
				),
			},
		},
		// Destroy un-manages; nothing is revoked.
		CheckDestroy: typeHas(mock, 102, i64p(2), false, true),
	})
}

// An undeclared inherited row written by a person is a row of its own: it is
// shown in the plan before any apply can touch it, and removed only then. A
// row on a preserved dimension, and CT's baseline, are never planned.
func TestAccGroupTypeRole_UndeclaredInheritedAndPreserved(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeRole(mock)
	cfg := providerBlock(mock.URL) + fmt.Sprintf(typeRoleConfig, "")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					mock.AddDomainPermission("group_type_role", typeRoleID, testmock.PermRow{AuthID: 702, DataID: i64p(9), Type: "grant", ModifiedPid: 1})
					mock.AddDomainPermission("group_type_role", typeRoleID, testmock.PermRow{AuthID: 1101, IsInherited: true, Type: "grant", ModifiedPid: -1})
				},
				Config:   cfg,
				PlanOnly: true, // preserved row + baseline: nothing to plan
			},
			{
				PreConfig: func() {
					mock.AddDomainPermission("group_type_role", typeRoleID, testmock.PermRow{AuthID: 10102, DataID: i64p(3), IsInherited: true, Type: "grant", ModifiedPid: 1})
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					lastWrites(mock, "DELETE 10102 inherited [3]"),
					typeHas(mock, 10102, i64p(3), true, false),
					typeHas(mock, 702, i64p(9), false, true),
					typeHas(mock, 1101, nil, true, true),
					typeHas(mock, 1101, nil, false, true),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

// Import by the bare role id, and with the preserve dimensions appended: the
// first plan after import is empty although an undeclared crp_query row is live.
func TestAccGroupTypeRole_Import(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeRole(mock)
	mock.AddDomainPermission("group_type_role", typeRoleID, testmock.PermRow{AuthID: 702, DataID: i64p(9), Type: "grant", ModifiedPid: 1})
	cfg := providerBlock(mock.URL) + fmt.Sprintf(typeRoleConfig, "")
	imp := `
import {
  to = churchtools_group_type_role.participant
  id = "160/crp_query"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg + imp,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "id", "160"),
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "role_id", "160"),
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "role", "participant"),
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "grant.#", "3"),
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "preserved_grants.#", "1"),
					func(*terraform.State) error {
						if w := mock.PermissionWrites(); len(w) != 0 {
							return fmt.Errorf("import wrote %q", w)
						}
						return nil
					},
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

// Import by the bare role id: without preserve_unknown in the id, the live
// crp_query row is a pending removal, which is what the plan must show.
func TestAccGroupTypeRole_ImportBareIDShowsUndeclared(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeRole(mock)
	mock.AddDomainPermission("group_type_role", typeRoleID, testmock.PermRow{AuthID: 702, DataID: i64p(9), Type: "grant", ModifiedPid: 1})
	cfg := providerBlock(mock.URL) + fmt.Sprintf(typeRoleConfig, "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config:             cfg + "\nimport {\n  to = churchtools_group_type_role.participant\n  id = \"160\"\n}\n",
			ExpectNonEmptyPlan: true,
			PlanOnly:           true,
		}},
	})
}

func TestAccGroupTypeRole_RoleMissingAndValidation(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeRole(mock)
	for name, c := range map[string]struct{ cfg, err string }{
		"missing role": {`resource "churchtools_group_type_role" "x" { role_id = "999" }`, `existiert nicht`},
		"bad id":       {`resource "churchtools_group_type_role" "x" { role_id = "abc" }`, `keine Rollen-id`},
		"wrong dimension": {`resource "churchtools_group_type_role" "x" {
  role_id = "160"
  grant {
    right  = "churchdb:view alldata"
    groups = ["2"]
  }
}`, `departments`},
	} {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6(),
				Steps:                    []resource.TestStep{{Config: providerBlock(mock.URL) + c.cfg, ExpectError: regexp.MustCompile(c.err)}},
			})
		})
	}
}

// The mock models CT's refusal; make sure it is really armed, or the lifecycle
// test above would prove nothing about the absence of deny rows.
func TestGroupTypeRoleMockRefusesRevoke(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedTypeRole(mock)
	req := `{"authId":102,"dataId":[2],"isInherited":false,"type":"revoke"}`
	resp, err := httpPut(mock.URL+"/api/permissions/group_type_role/160", req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp, "400") {
		t.Fatalf("PUT type=revoke on group_type_role answered %s, want 400", resp)
	}
}

func httpPut(url, body string) (string, error) {
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	return fmt.Sprint(res.StatusCode), nil
}
