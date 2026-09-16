package provider_test

import (
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// ChurchTools rejects a group-type create that omits these fields. ct-cli
// supplies them as createDefaults; the provider must too, or every apply that
// creates a type 422s. They are NOT managed fields — never diffed, sent once.
func TestAccGroupType_SendsCreateDefaults(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_group_type" "dienst" {
  name            = "Dienst"
  name_translated = "Dienst"
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("churchtools_group_type.dienst", "name", "Dienst"),
				resource.TestCheckResourceAttrSet("churchtools_group_type.dienst", "id"),
			),
		}},
	})

	row := mock.Find("/group/grouptypes", "name", "Dienst")
	if row == nil {
		t.Fatal("group type was not created on the mock")
	}
	for key, want := range map[string]any{
		"color":                 "default",
		"permissionDepth":       float64(1),
		"isLeaderNecessary":     false,
		"availableForNewPerson": false,
		"postsEnabled":          false,
		"sortKey":               float64(0),
	} {
		if row[key] != want {
			t.Errorf("create body %s = %v (%T), want %v", key, row[key], row[key], want)
		}
	}
	// namePlural/shorty are padded/truncated from the name.
	if row["shorty"] != "Dienst" {
		t.Errorf("shorty = %v, want Dienst", row["shorty"])
	}
}

func TestAccPersonStatus_RoundTrip(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_person_status" "mitglied" {
  name              = "Mitglied"
  shorty            = "M"
  is_member         = true
  is_searchable     = true
  sort_key          = 10
  security_level_id = 1
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("churchtools_person_status.mitglied", "is_member", "true"),
				resource.TestCheckResourceAttr("churchtools_person_status.mitglied", "sort_key", "10"),
				resource.TestCheckResourceAttr("churchtools_person_status.mitglied", "security_level_id", "1"),
			),
		}},
	})
}

func TestAccDepartment_SortKeyDefaultsToZero(t *testing.T) {
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
			Check: resource.TestCheckResourceAttr("churchtools_department.musik", "sort_key", "0"),
		}},
	})
}

func TestAccCommentViewer_RoundTrip(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_comment_viewer" "leitung" {
  name     = "Leitung"
  sort_key = 1
}`,
			Check: resource.TestCheckResourceAttr("churchtools_comment_viewer.leitung", "name", "Leitung"),
		}},
	})
}
