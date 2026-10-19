package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// Mock acceptance tests for churchtools_group_role. They run under TF_ACC like
// every other test here, against the in-process mock, which models CT's
// permission semantics (testmock/permissions.go). Nothing here reaches a live
// instance; the live check is the manual protocol in the PR / README.

func i64p(v int64) *int64 { return &v }

// sll_koblenz "Mitglied" on eqrm-dev (pairing 16829), as captured 2026-10-19.
func seedSLL(mock *testmock.Server) {
	mock.SeedGroupRoles(1748, map[string]int{"Mitglied": 16829, "Leiter": 16830})
	mock.SeedPermissions(16829, []testmock.PermRow{
		{AuthID: 102, DataID: i64p(2), Type: "grant", ModifiedPid: 1},
		{AuthID: 124, DataID: i64p(24), Type: "grant", ModifiedPid: 1},
	})
}

const sllConfig = `
resource "churchtools_group_role" "mitglied" {
  group_id         = "1748"
  role             = "Mitglied"
  preserve_unknown = ["crp_query"]

  grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
  grant {
    right    = "churchdb:view station"
    campuses = ["24"]
  }
  %s
}
`

const queryGrant = `grant {
    right    = "churchreport:view query"
    data_ids = ["3"]
  }`

func hasRow(rows []testmock.PermRow, auth int64, data *int64) bool {
	for _, r := range rows {
		if r.AuthID == auth && ((data == nil && r.DataID == nil) || (data != nil && r.DataID != nil && *r.DataID == *data)) {
			return true
		}
	}
	return false
}

func lastWrites(mock *testmock.Server, want ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got := mock.PermissionWrites()
		if len(got) < len(want) || strings.Join(got[len(got)-len(want):], "; ") != strings.Join(want, "; ") {
			return fmt.Errorf("permission writes %q, want them to end with %q", got, want)
		}
		return nil
	}
}

func liveHas(mock *testmock.Server, auth int64, data *int64, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if hasRow(mock.Permissions(16829), auth, data) != want {
			return fmt.Errorf("row %d %v present = %v, want %v; rows %+v", auth, data, !want, want, mock.Permissions(16829))
		}
		return nil
	}
}

// The spike: an undeclared grant on a preserved dimension, added "in the UI"
// between runs, never shows as a pending removal, over three plans; an
// undeclared grant on any other dimension does, and the apply revokes exactly
// that row.
func TestAccGroupRole_PreserveUnknown(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	cfg := providerBlock(mock.URL) + fmt.Sprintf(sllConfig, queryGrant)
	addUIRow := func() {
		mock.AddPermission(16829, testmock.PermRow{AuthID: 702, DataID: i64p(9), Type: "grant", ModifiedPid: 1})
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "id", "16829"),
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grant.#", "3"),
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "preserved_grants.#", "0"),
					// Create sends only the missing row; 102/124 were live already.
					lastWrites(mock, "PUT 702 [3]"),
					liveHas(mock, 702, i64p(3), true),
				),
			},
			{PreConfig: addUIRow, Config: cfg, PlanOnly: true},
			{Config: cfg, PlanOnly: true},
			{Config: cfg, PlanOnly: true},
			{
				// A refresh+apply round shows the preserved row for review and
				// still sends nothing.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "preserved_grants.#", "1"),
					resource.TestCheckTypeSetElemAttr("churchtools_group_role.mitglied", "preserved_grants.*", "churchreport:view query [9]"),
					lastWrites(mock, "PUT 702 [3]"),
				),
			},
			{
				// Undeclared, unscoped, not preservable: the plan proposes the revoke.
				PreConfig:          func() { mock.AddPermission(16829, testmock.PermRow{AuthID: 1101, Type: "grant", ModifiedPid: 1}) },
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					lastWrites(mock, "DELETE 1101"),
					liveHas(mock, 1101, nil, false),
					liveHas(mock, 702, i64p(9), true),
				),
			},
			{
				// Undeclaring an owned row on the preserved dimension revokes that
				// row, by exact id; the UI row next to it survives. The plan keeps
				// preserved_grants known: the change does not touch it.
				Config: providerBlock(mock.URL) + fmt.Sprintf(sllConfig, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectKnownValue("churchtools_group_role.mitglied", tfjsonpath.New("preserved_grants"),
						knownvalue.SetExact([]knownvalue.Check{knownvalue.StringExact("churchreport:view query [9]")})),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					lastWrites(mock, "DELETE 702 [3]"),
					liveHas(mock, 702, i64p(3), false),
					liveHas(mock, 702, i64p(9), true),
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grant.#", "2"),
				),
			},
			{
				// Declaring the preserved row takes it out of preserved_grants, so
				// that plan leaves the attribute unknown.
				Config: providerBlock(mock.URL) + fmt.Sprintf(sllConfig, `grant {
    right    = "churchreport:view query"
    data_ids = ["9"]
  }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectUnknownValue("churchtools_group_role.mitglied", tfjsonpath.New("preserved_grants")),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "preserved_grants.#", "0"),
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grant.#", "3"),
				),
			},
		},
		// Destroy un-manages; the grants stay in ChurchTools.
		CheckDestroy: func(*terraform.State) error {
			if !hasRow(mock.Permissions(16829), 102, i64p(2)) || !hasRow(mock.Permissions(16829), 702, i64p(9)) {
				return fmt.Errorf("destroy revoked grants: %+v", mock.Permissions(16829))
			}
			return nil
		},
	})
}

// Without preserve_unknown the same UI row on crp_query is a pending removal.
func TestAccGroupRole_NoPreserveRevokes(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	cfg := providerBlock(mock.URL) + strings.Replace(fmt.Sprintf(sllConfig, ""), `preserve_unknown = ["crp_query"]`, "", 1)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					mock.AddPermission(16829, testmock.PermRow{AuthID: 702, DataID: i64p(9), Type: "grant", ModifiedPid: 1})
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg, Check: lastWrites(mock, "DELETE 702 [9]")},
		},
	})
}

// Import with the preserve dimensions in the id: the first plan after import
// is empty although an undeclared crp_query row is live.
func TestAccGroupRole_Import(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.AddPermission(16829, testmock.PermRow{AuthID: 702, DataID: i64p(9), Type: "grant", ModifiedPid: 1})
	// direct and subgroup rows of the same right are distinct grants (#25)
	mock.AddPermission(16829, testmock.PermRow{AuthID: 10101, DataID: i64p(3), Type: "grant", ModifiedPid: 1})
	mock.AddPermission(16829, testmock.PermRow{AuthID: 10101, DataID: i64p(3), IsInherited: true, Type: "grant", ModifiedPid: 1})
	cfg := providerBlock(mock.URL) + fmt.Sprintf(sllConfig, `grant {
    right           = "churchdb:+see persons"
    security_levels = ["3"]
  }
  grant {
    right                = "churchdb:+see persons"
    applies_to_subgroups = true
    security_levels      = ["3"]
  }`)
	imp := `
import {
  to = churchtools_group_role.mitglied
  id = "1748/Mitglied/crp_query"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg + imp,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "id", "16829"),
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grant.#", "4"),
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "preserved_grants.#", "1"),
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

// The same declaration on a host that lacks the module: the grant is skipped
// with a warning, never written, never read back, and the plan stays clean over
// repeated runs. The strict neighbours are still applied.
func TestAccGroupRole_UnofferedRightSkipped(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.SetInactiveAuthIDs(701, 702, 2080)
	cfg := providerBlock(mock.URL) + fmt.Sprintf(sllConfig, `grant { right = "churchreport:view" }
  grant { right = "jpmFlowManagerPreview:view" }`)
	noUnofferedWrite := func(*terraform.State) error {
		for _, w := range mock.PermissionWrites() {
			if strings.HasPrefix(w, "PUT 701") || strings.HasPrefix(w, "PUT 2080") {
				return fmt.Errorf("wrote an unoffered right: %q", w)
			}
		}
		if mock.AuthTableCalls() == 0 {
			return fmt.Errorf("the host's right table was never asked for")
		}
		return nil
	}
	clean := resource.TestStep{Config: cfg, PlanOnly: true}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.ComposeAggregateTestCheckFunc(
				noUnofferedWrite,
				resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "grant.#", "4"),
			)},
			clean, clean, clean, // plan stability over three runs
			{Config: cfg, ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectEmptyPlan(),
			}}},
		},
	})
}

// A right the host offers keeps the strict guard even when the host lacks
// OTHER modules: 204 without a stored row is an error.
func TestAccGroupRole_OfferedRightStaysStrict(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.SetInactiveAuthIDs(2080)
	mock.SetDroppedAuthIDs(701)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config:      providerBlock(mock.URL) + fmt.Sprintf(sllConfig, `grant { right = "churchreport:view" }`),
			ExpectError: regexp.MustCompile(`nicht gespeichert`),
		}},
	})
}

// If the host's right table cannot be read, nothing is skipped: the grant takes
// the strict path and its missing read-back is an error naming the reason.
func TestAccGroupRole_UnofferedFailsClosed(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.SetInactiveAuthIDs(701)
	mock.FailAuthTable()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config:      providerBlock(mock.URL) + fmt.Sprintf(sllConfig, `grant { right = "churchreport:view" }`),
			ExpectError: regexp.MustCompile(`nicht gespeichert(.|\n)*nichts\s+übersprungen`),
		}},
	})
	if w := mock.PermissionWrites(); strings.Join(w, "; ") != "PUT 701" {
		t.Fatalf("writes %q, want only the strict PUT 701", w)
	}
}

// An unreadable right table must not block grants the host stores: adding
// one, or refreshing over one deleted in the UI (a token without churchauth
// access, a failing session handshake).
func TestAccGroupRole_StoredGrantsNeedNoRightTable(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	mock.FailAuthTable()
	cfg := providerBlock(mock.URL) + fmt.Sprintf(sllConfig, queryGrant)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: lastWrites(mock, "PUT 702 [3]")},
			{
				// Deleted in the UI: plain drift, the plan re-adds it.
				PreConfig:          func() { mock.SeedPermissions(16829, mock.Permissions(16829)[:2]) },
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// A role with no grant blocks on a pairing with no rows plans clean.
func TestAccGroupRole_Empty(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	cfg := providerBlock(mock.URL) + `
resource "churchtools_group_role" "leiter" {
  group_id = "1748"
  role     = "Leiter"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr("churchtools_group_role.leiter", "id", "16830")},
			{Config: cfg, PlanOnly: true},
		},
	})
}

func TestAccGroupRole_Validation(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	cases := map[string]struct{ body, err string }{
		"unknown role": {`grant { right = "churchcore:administer church html templates" }`, `keine Rolle "Nope"`},
		"wrong dimension": {`grant {
    right  = "churchdb:view alldata"
    groups = ["2"]
  }`, `departments`},
		"explicit false": {`grant {
    right                = "churchcore:administer church html templates"
    applies_to_subgroups = false
  }`, `false`},
		"duplicate": {`grant {
    right       = "churchdb:view alldata"
    departments = ["2"]
  }
  grant {
    right       = "churchdb:view alldata"
    departments = ["3"]
  }`, `doppelt`},
		"unknown right": {`grant { right = "churchdb:fly" }`, `Unbekanntes Recht`},
		"unscoped with ids": {`grant {
    right    = "churchcore:administer church html templates"
    data_ids = ["1"]
  }`, `keine Daten-Auswahl`},
		"empty ids": {`grant {
    right       = "churchdb:view alldata"
    departments = []
  }`, `Leere Auswahl`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			role := "Mitglied"
			if name == "unknown role" {
				role = "Nope"
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6(),
				Steps: []resource.TestStep{{
					Config: providerBlock(mock.URL) + fmt.Sprintf(`
resource "churchtools_group_role" "x" {
  group_id = "1748"
  role     = %q
  %s
}
`, role, c.body),
					ExpectError: regexp.MustCompile(c.err),
				}},
			})
		})
	}
	t.Run("unknown preserve dimension", func(t *testing.T) {
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6(),
			Steps: []resource.TestStep{{
				Config: providerBlock(mock.URL) + `
resource "churchtools_group_role" "x" {
  group_id         = "1748"
  role             = "Mitglied"
  preserve_unknown = ["crp_querry"]
}
`,
				ExpectError: regexp.MustCompile(`Unbekannte Dimension`),
			}},
		})
	})
}

// A row surfaced as a pending removal is NOT revoked by the apply that adds
// its dimension to preserve_unknown: that change means "leave those alone".
func TestAccGroupRole_AddingPreserveAdopts(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedSLL(mock)
	without := providerBlock(mock.URL) + strings.Replace(fmt.Sprintf(sllConfig, ""), `preserve_unknown = ["crp_query"]`, "", 1)
	with := providerBlock(mock.URL) + fmt.Sprintf(sllConfig, "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: without},
			{
				PreConfig: func() {
					mock.AddPermission(16829, testmock.PermRow{AuthID: 702, DataID: i64p(9), Type: "grant", ModifiedPid: 1})
				},
				Config:             without,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: with,
				Check: resource.ComposeAggregateTestCheckFunc(
					liveHas(mock, 702, i64p(9), true),
					resource.TestCheckResourceAttr("churchtools_group_role.mitglied", "preserved_grants.#", "1"),
				),
			},
			{Config: with, PlanOnly: true},
		},
	})
}
