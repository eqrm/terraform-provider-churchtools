package provider_test

import (
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// seedPrivacyTypes reproduces the legacy payload faithfully: every value is a
// string, ids included.
func seedPrivacyTypes(mock *testmock.Server) {
	mock.SeedLegacy("privacy_policy_agreement_types", "4", map[string]any{
		"bezeichnung": "privacy.policy.agreement.type.checkin", "deletable": "0", "sortkey": "40",
	})
}

// The dev case of ct-structure#121: "Connect-App" exists on prod only, so the
// dev apply CREATES it — through saveMasterData, which returns no id.
func TestAccPrivacyAgreementType_CreateRecoversIDByDiff(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedPrivacyTypes(mock)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_privacy_agreement_type" "adult" {
  name     = "Connect-App"
  sort_key = 70
}`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet("churchtools_privacy_agreement_type.adult", "id"),
					resource.TestCheckResourceAttr("churchtools_privacy_agreement_type.adult", "sort_key", "70"),
					resource.TestCheckResourceAttr("churchtools_privacy_agreement_type.adult", "deletable", "true"),
				),
			},
			// The row read back through getMasterData equals what was written.
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_privacy_agreement_type" "adult" {
  name     = "Connect-App"
  sort_key = 70
}`,
				PlanOnly: true,
			},
		},
	})
}

// The prod case and the built-in: import by id, plan nothing, and an update
// addresses THAT row (a set id) without touching `deletable`.
func TestAccPrivacyAgreementType_ImportBuiltInThenUpdate(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedPrivacyTypes(mock)

	base := providerBlock(mock.URL)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: base + `
resource "churchtools_privacy_agreement_type" "child" {
  name = "privacy.policy.agreement.type.checkin"
}
import {
  to = churchtools_privacy_agreement_type.child
  id = "4"
}`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_privacy_agreement_type.child", "sort_key", "40"),
					resource.TestCheckResourceAttr("churchtools_privacy_agreement_type.child", "deletable", "false"),
				),
			},
			{
				Config: base + `
resource "churchtools_privacy_agreement_type" "child" {
  name = "privacy.policy.agreement.type.checkin"
}`,
				PlanOnly: true,
			},
			{
				Config: base + `
resource "churchtools_privacy_agreement_type" "child" {
  name     = "privacy.policy.agreement.type.checkin"
  sort_key = 45
}`,
				Check: resource.TestCheckResourceAttr("churchtools_privacy_agreement_type.child", "deletable", "false"),
			},
		},
	})

	rows := mock.LegacyRows("privacy_policy_agreement_types")
	if len(rows) != 1 {
		t.Fatalf("the update created a row instead of editing #4: %v", rows)
	}
	if rows["4"]["sortkey"] != "45" || rows["4"]["deletable"] != "0" {
		t.Errorf("row 4 after update = %v, want sortkey 45 and deletable untouched (0)", rows["4"])
	}
}

// A second row of an existing name could not be told apart afterwards, since the
// create returns no id. Refuse, and point at import instead.
func TestAccPrivacyAgreementType_SameNameCreateRefused(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedPrivacyTypes(mock)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_privacy_agreement_type" "x" {
  name = "privacy.policy.agreement.type.checkin"
}`,
			ExpectError: regexp.MustCompile(`(?s)existiert bereits.*tofu import`),
		}},
	})
}

// saveMasterData returns no id, so Create finds its row by diffing snapshots.
// Terraform creates independent resources in PARALLEL; two snapshot→write→diff
// sequences that interleave each see two new rows and neither can claim one.
func TestAccPrivacyAgreementType_ParallelCreatesEachFindTheirRow(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedPrivacyTypes(mock)

	config := providerBlock(mock.URL)
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		config += `
resource "churchtools_privacy_agreement_type" "` + n + `" {
  name = "Typ ` + n + `"
}`
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps:                    []resource.TestStep{{Config: config}},
	})
}
