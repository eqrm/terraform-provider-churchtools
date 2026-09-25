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
	mock.SeedLegacy("privacy_policy_agreement_types", "6", map[string]any{
		"bezeichnung": "Connect-App", "deletable": "1", "sortkey": "70",
	})
}

func TestAccPrivacyAgreementType_ResolvesByName(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedPrivacyTypes(mock)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
data "churchtools_privacy_agreement_type" "adult" {
  name = "Connect-App"
}
data "churchtools_privacy_agreement_type" "child" {
  name = "privacy.policy.agreement.type.checkin"
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("data.churchtools_privacy_agreement_type.adult", "id", "6"),
				resource.TestCheckResourceAttr("data.churchtools_privacy_agreement_type.adult", "sort_key", "70"),
				resource.TestCheckResourceAttr("data.churchtools_privacy_agreement_type.adult", "deletable", "true"),
				resource.TestCheckResourceAttr("data.churchtools_privacy_agreement_type.child", "id", "4"),
				resource.TestCheckResourceAttr("data.churchtools_privacy_agreement_type.child", "deletable", "false"),
			),
		}},
	})
}

// A missing type must fail loudly and name what IS there -- the realistic case
// is an instance that never had the custom "Connect-App" row.
func TestAccPrivacyAgreementType_MissingFailsWithInventory(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedPrivacyTypes(mock)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
data "churchtools_privacy_agreement_type" "x" {
  name = "Import aus Churchsuite"
}`,
			ExpectError: regexp.MustCompile(`(?s)nicht gefunden.*Connect-App`),
		}},
	})
}

func TestAccPrivacyAgreementType_AmbiguousNameFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedPrivacyTypes(mock)
	mock.SeedLegacy("privacy_policy_agreement_types", "9", map[string]any{
		"bezeichnung": "Connect-App", "deletable": "1", "sortkey": "80",
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
data "churchtools_privacy_agreement_type" "x" {
  name = "Connect-App"
}`,
			ExpectError: regexp.MustCompile(`mehrdeutig`),
		}},
	})
}
