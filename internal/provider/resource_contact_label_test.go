package provider_test

import (
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// CT requires name, sortKey AND isDefault on create; an omitted pair must go out
// as CT's own defaults, not be dropped.
func TestAccContactLabel_CreateSendsRequiredDefaults(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_contact_label" "eltern" {
  name = "Eltern"
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttrSet("churchtools_contact_label.eltern", "id"),
				resource.TestCheckResourceAttr("churchtools_contact_label.eltern", "sort_key", "0"),
				resource.TestCheckResourceAttr("churchtools_contact_label.eltern", "is_default", "false"),
			),
		}},
	})

	row := mock.Find("/contactlabels", "name", "Eltern")
	if row == nil {
		t.Fatal("label was not created on the mock")
	}
	if row["sortKey"] != float64(0) || row["isDefault"] != false {
		t.Errorf("create body sortKey=%v isDefault=%v, want 0/false", row["sortKey"], row["isDefault"])
	}
}

// Importing an existing label with a config that names only `name` must plan
// nothing: sort_key/is_default are Optional+Computed and keep what CT has.
// Then a rename must still resend the unmanaged pair, because PUT requires all
// three fields and would otherwise reset them.
func TestAccContactLabel_ImportThenRenameKeepsUnmanagedFields(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/contactlabels", 6, map[string]any{"name": "Eltern", "sortKey": float64(30), "isDefault": false})
	mock.Seed("/contactlabels", 1, map[string]any{"name": "contact.label.private", "sortKey": float64(10), "isDefault": true})

	importBlock := `
import {
  to = churchtools_contact_label.eltern
  id = "6"
}
import {
  to = churchtools_contact_label.privat
  id = "1"
}`
	base := providerBlock(mock.URL) + `
resource "churchtools_contact_label" "privat" {
  name = "contact.label.private"
}`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: base + `
resource "churchtools_contact_label" "eltern" {
  name = "Eltern"
}` + importBlock,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_contact_label.eltern", "sort_key", "30"),
					resource.TestCheckResourceAttr("churchtools_contact_label.privat", "is_default", "true"),
				),
			},
			{
				Config: base + `
resource "churchtools_contact_label" "eltern" {
  name = "Eltern"
}`,
				PlanOnly: true,
			},
			{
				Config: base + `
resource "churchtools_contact_label" "eltern" {
  name = "Erziehungsberechtigte"
}`,
			},
		},
	})

	row := mock.Find("/contactlabels", "name", "Erziehungsberechtigte")
	if row == nil {
		t.Fatal("rename did not reach the mock")
	}
	if row["sortKey"] != float64(30) || row["isDefault"] != false {
		t.Errorf("rename reset unmanaged fields: sortKey=%v isDefault=%v, want 30/false", row["sortKey"], row["isDefault"])
	}
	if p := mock.Find("/contactlabels", "name", "contact.label.private"); p == nil || p["isDefault"] != true {
		t.Errorf("the default label changed: %v", p)
	}
}

func TestAccContactLabel_CreateWithoutIDFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.DropCreateID("/contactlabels")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_contact_label" "x" {
  name = "Eltern"
}`,
			ExpectError: regexp.MustCompile(`keine id zur`),
		}},
	})
}
