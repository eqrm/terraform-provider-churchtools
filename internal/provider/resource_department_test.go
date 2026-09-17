package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// A Bereich create must go through the LEGACY master-data endpoint and then
// learn its id by diffing GET /departments, because saveMasterData returns no
// id. If this ever regresses to POST /departments, the mock answers 405.
func TestAccDepartment_CreatesViaLegacyEndpoint(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name   = "Bereich Musik"
  shorty = "MU"
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("churchtools_department.musik", "name", "Bereich Musik"),
				resource.TestCheckResourceAttr("churchtools_department.musik", "shorty", "MU"),
				// ct-cli stores sortKey as `?? 0`; matching it keeps an export
				// that omits the field a no-op instead of a diff.
				resource.TestCheckResourceAttr("churchtools_department.musik", "sort_key", "0"),
				resource.TestCheckResourceAttrSet("churchtools_department.musik", "id"),
			),
		}},
	})

	if row := mock.Find("/departments", "name", "Bereich Musik"); row == nil {
		t.Fatal("the Bereich never reached /departments — the legacy write did not land")
	}
}

func TestAccDepartment_UpdatesByID(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name     = "Bereich Musik"
  shorty   = "MU"
  sort_key = 3
}`,
			},
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name     = "Bereich Musik und Technik"
  shorty   = "MT"
  sort_key = 4
}`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_department.musik", "name", "Bereich Musik und Technik"),
					resource.TestCheckResourceAttr("churchtools_department.musik", "sort_key", "4"),
				),
			},
		},
	})

	if mock.Find("/departments", "name", "Bereich Musik") != nil {
		t.Error("the old row survived — the update created a second Bereich instead of updating by id")
	}
}

// The legacy endpoint returns no id, so a second Bereich of the same name could
// not be told from the first — and the create would already have succeeded,
// leaving an orphan that every retry duplicates. Refuse before writing.
func TestAccDepartment_RefusesDuplicateName(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/departments", 3, map[string]any{"name": "Bereich Musik", "shorty": "MU", "sortKey": float64(0)})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name   = "Bereich Musik"
  shorty = "MU"
}`,
			ExpectError: regexp.MustCompile(`Bereich existiert bereits`),
		}},
	})
}

// The duplicate-name guard filters GET /departments, which CT pages. If List
// stops at page 1 the guard never sees a Bereich that sits further down, passes,
// and SaveMasterData writes a SECOND Bereich with the same name -- the exact
// corruption the guard exists to prevent. Seed well past one page and put the
// clashing name last.
func TestAccDepartment_RefusesDuplicateBeyondFirstPage(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	for i := 1; i <= 250; i++ {
		mock.Seed("/departments", i, map[string]any{
			"name": fmt.Sprintf("Bereich %d", i), "shorty": "B", "sortKey": float64(0),
		})
	}
	mock.Seed("/departments", 900, map[string]any{"name": "Bereich Technik", "shorty": "TE", "sortKey": float64(0)})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_department" "technik" {
  name   = "Bereich Technik"
  shorty = "TE"
}`,
			ExpectError: regexp.MustCompile(`Bereich existiert bereits`),
		}},
	})
}
