package provider_test

import (
	"fmt"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// A ChurchTools system baseline row (modifiedPid -1) SATISFIES a declared row
// with the same identity (authId, dataId, isInherited); the provider never
// revokes it. Shared engine, so group_role and status behave alike (covered
// at unit level in internal/grants).

func seedSystemTypeRole(mock *testmock.Server) {
	seedTypeRole(mock)
	mock.SeedDomainPermissions("group_type_role", typeRoleID, []testmock.PermRow{
		{AuthID: 1101, Type: "grant", ModifiedPid: -1},
		{AuthID: 10101, DataID: i64p(3), IsInherited: true, Type: "grant", ModifiedPid: -1},
		// Undeclared system row: never in state, never revoked.
		{AuthID: 10101, DataID: i64p(4), IsInherited: true, Type: "grant", ModifiedPid: -1},
	})
}

const systemDeclared = `
resource "churchtools_group_type_role" "participant" {
  role_id = "160"

  grant {
    right = "churchgroup:view"
  }
  grant {
    right                = "churchdb:+see persons"
    applies_to_subgroups = true
    security_levels      = ["3"]
  }
  grant {
    right                = "churchdb:+see group"
    applies_to_subgroups = true
    security_levels      = ["3"]
  }
}
`

func noWrites(mock *testmock.Server) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if w := mock.PermissionWrites(); len(w) != 0 {
			return fmt.Errorf("expected no permission writes, got %q", w)
		}
		return nil
	}
}

// Declared + system row: no write, no diff. Declared + absent: written (PUT).
// Undeclared system row: untouched. Dropped from config: not revoked.
func TestAccGroupTypeRole_SystemRowSatisfiesDeclared(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSystemTypeRole(mock)
	cfg := providerBlock(mock.URL) + systemDeclared
	dropped := providerBlock(mock.URL) + `
resource "churchtools_group_type_role" "participant" {
  role_id = "160"

  grant {
    right                = "churchdb:+see group"
    applies_to_subgroups = true
    security_levels      = ["3"]
  }
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "grant.#", "3"),
					lastWrites(mock, "PUT 10102 inherited [3]"),
					func(*terraform.State) error {
						if n := len(mock.PermissionWrites()); n != 1 {
							return fmt.Errorf("want exactly one write, got %q", mock.PermissionWrites())
						}
						return nil
					},
					typeHas(mock, 10101, i64p(4), true, true),
				),
			},
			{Config: cfg, PlanOnly: true}, // satisfied: clean plan
			{
				// Dropping declared system rows must not revoke them.
				Config: dropped,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "grant.#", "1"),
					typeHas(mock, 1101, nil, false, true),
					typeHas(mock, 10101, i64p(3), true, true),
					typeHas(mock, 10101, i64p(4), true, true),
				),
			},
			{Config: dropped, PlanOnly: true},
			{
				// Declared again: satisfied by the still-live system row, no write.
				PreConfig: func() { mock.ClearPermissionWrites() },
				Config:    cfg,
				Check:     noWrites(mock),
			},
		},
	})
}

// Import must not pull undeclared system rows into state; a system row the
// config declares afterwards shows up once as an addition, is found live, and
// is adopted without a write.
func TestAccGroupTypeRole_SystemRowImportThenDeclare(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSystemTypeRole(mock)
	cfg := providerBlock(mock.URL) + `
resource "churchtools_group_type_role" "participant" {
  role_id = "160"

  grant {
    right = "churchgroup:view"
  }
}
import {
  to = churchtools_group_type_role.participant
  id = "160"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_type_role.participant", "grant.#", "1"),
					noWrites(mock),
					typeHas(mock, 10101, i64p(3), true, true),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}
