package provider_test

import (
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// An omitted sort_key must reach ChurchTools as 0 on create, not be dropped —
// same contract as contact labels and comment viewers.
func TestAccTargetGroup_CreateDefaultsSortKey(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_target_group" "keine" {
  name = "Keine"
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttrSet("churchtools_target_group.keine", "id"),
				resource.TestCheckResourceAttr("churchtools_target_group.keine", "sort_key", "0"),
			),
		}},
	})

	row := mock.Find("/group/targetgroups", "name", "Keine")
	if row == nil {
		t.Fatal("target group was not created on the mock")
	}
	if row["sortKey"] != float64(0) {
		t.Errorf("create body sortKey=%v, want 0", row["sortKey"])
	}
}

// Importing a row whose config names only `name` must plan nothing: sort_key is
// Optional+Computed, so it keeps what ChurchTools has instead of renumbering the
// instance's list to 0 on the next apply. This is the bug the whole
// Optional+Computed-without-default pattern exists to prevent.
func TestAccTargetGroup_ImportKeepsServerSortKey(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/group/targetgroups", 33, map[string]any{"name": "Keine", "sortKey": float64(10)})

	cfg := providerBlock(mock.URL) + `
import {
  to = churchtools_target_group.keine
  id = "33"
}
resource "churchtools_target_group" "keine" {
  name = "Keine"
}`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_target_group.keine", "id", "33"),
					resource.TestCheckResourceAttr("churchtools_target_group.keine", "sort_key", "10"),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}

// `start` and `end` are required by the API on create — a POST without them is a
// 400 — so they must actually reach the wire, not be silently omitted.
func TestAccAgeGroup_CreateSendsStartAndEnd(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_age_group" "nextgen" {
  name     = "NextGen"
  sort_key = 8
  start    = 0
  end      = 99
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("churchtools_age_group.nextgen", "start", "0"),
				resource.TestCheckResourceAttr("churchtools_age_group.nextgen", "end", "99"),
			),
		}},
	})

	row := mock.Find("/group/agegroups", "name", "NextGen")
	if row == nil {
		t.Fatal("age group was not created on the mock")
	}
	if row["start"] != float64(0) || row["end"] != float64(99) {
		t.Errorf("create body start=%v end=%v, want 0/99", row["start"], row["end"])
	}
	if row["sortKey"] != float64(8) {
		t.Errorf("create body sortKey=%v, want 8", row["sortKey"])
	}
}

// `color` is required by the API and validated server-side against an enum.
// It must be sent verbatim; and an unset description must NOT go out as "",
// which would blank whatever the instance has.
func TestAccGroupCategory_CreateSendsColorAndOmitsUnsetDescription(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_group_category" "egroup" {
  name     = "eGroup"
  sort_key = 30
  color    = "orange"
}`,
			Check: resource.TestCheckResourceAttr("churchtools_group_category.egroup", "color", "orange"),
		}},
	})

	row := mock.Find("/group/groupcategories", "name", "eGroup")
	if row == nil {
		t.Fatal("category was not created on the mock")
	}
	if row["color"] != "orange" {
		t.Errorf("create body color=%v, want orange", row["color"])
	}
	if _, sent := row["description"]; sent {
		t.Errorf("create body carried description=%v; an unset description must be omitted, not blanked", row["description"])
	}
}

// Every privacy_agreement_who row is a ChurchTools built-in, identical on every
// instance. Create must refuse and point at import rather than attempting a
// legacy write — the table is deliberately NOT on the client's write allowlist,
// so an attempt would fail later and less legibly.
func TestAccPrivacyAgreementWho_CreateRefusesAndPointsAtImport(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_privacy_agreement_who" "own" {
}`,
			ExpectError: regexp.MustCompile(`kann nicht angelegt werden`),
		}},
	})
}

// The importable path: the built-in id resolves to its translation key and sort
// key, read out of the legacy getMasterData row set.
func TestAccPrivacyAgreementWho_ImportReadsBuiltIn(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.SeedLegacy("privacy_policy_agreement_who", "1", map[string]any{
		"id": "1", "bezeichnung": "privacy.policy.agreement.who.own", "sortkey": "10", "deletable": "0",
	})

	cfg := providerBlock(mock.URL) + `
import {
  to = churchtools_privacy_agreement_who.own
  id = "1"
}
resource "churchtools_privacy_agreement_who" "own" {
}`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_privacy_agreement_who.own", "id", "1"),
					resource.TestCheckResourceAttr("churchtools_privacy_agreement_who.own", "name", "privacy.policy.agreement.who.own"),
					resource.TestCheckResourceAttr("churchtools_privacy_agreement_who.own", "sort_key", "10"),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
}
