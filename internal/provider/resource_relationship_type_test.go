package provider_test

import (
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// The pickup-authorisation type the kids check-in reads. Creating it is the
// reason this resource exists: one instance has it, the other does not.
func TestAccRelationshipType_CreatePickupType(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_relationship_type" "pickup" {
  name              = "Abholberechtigter/Gastkind"
  degree_name_a     = "Abholung durch"
  degree_name_b     = "Abholberechtigt für"
  security_level_id = 1
  sort_key          = 2
}`,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttrSet("churchtools_relationship_type.pickup", "id"),
				resource.TestCheckResourceAttr("churchtools_relationship_type.pickup", "include_in_export", "false"),
				resource.TestCheckNoResourceAttr("churchtools_relationship_type.pickup", "export_title"),
			),
		}},
	})

	row := mock.Find("/person/relationshiptypes", "degreeNameB", "Abholberechtigt für")
	if row == nil {
		t.Fatal("type was not created on the mock")
	}
	if row["securityLevelId"] != float64(1) || row["sortKey"] != float64(2) || row["exportTitle"] != nil {
		t.Errorf("create body = %v", row)
	}
}

// One host-portable declaration of a BUILT-IN type must plan nothing on both
// instances, although they differ in sortKey (1 vs 10) and exportTitle (null vs
// ""). That is why those attributes are Optional+Computed without defaults.
func TestAccRelationshipType_BuiltInIsHostPortable(t *testing.T) {
	for _, seed := range []map[string]any{
		{"sortKey": float64(1), "exportTitle": nil},
		{"sortKey": float64(10), "exportTitle": ""},
	} {
		mock := testmock.New()
		t.Cleanup(mock.Close)
		row := map[string]any{
			"name":            "relationship.parent-child",
			"degreeNameA":     "relationship.part.parent",
			"degreeNameB":     "relationship.part.child",
			"securityLevelId": float64(1),
			"includeInExport": false,
		}
		for k, v := range seed {
			row[k] = v
		}
		mock.Seed("/person/relationshiptypes", 1, row)

		config := providerBlock(mock.URL) + `
resource "churchtools_relationship_type" "parent_child" {
  degree_name_a     = "relationship.part.parent"
  degree_name_b     = "relationship.part.child"
  security_level_id = 1
}`
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6(),
			Steps: []resource.TestStep{
				{Config: config + `
import {
  to = churchtools_relationship_type.parent_child
  id = "1"
}`},
				{Config: config, PlanOnly: true},
			},
		})
	}
}

// PUT treats an omitted name/exportTitle as null. An update that changes only a
// side label must therefore resend both, or it would silently wipe them.
func TestAccRelationshipType_UpdateKeepsNameAndExportTitle(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/person/relationshiptypes", 2, map[string]any{
		"name":            "relationship.couple",
		"degreeNameA":     "relationship.part.spouse",
		"degreeNameB":     "relationship.part.spouse",
		"securityLevelId": float64(1),
		"sortKey":         float64(0),
		"exportTitle":     "relationship.couple",
		"includeInExport": true,
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: providerBlock(mock.URL) + `
resource "churchtools_relationship_type" "couple" {
  degree_name_a     = "relationship.part.spouse"
  degree_name_b     = "relationship.part.spouse"
  security_level_id = 1
}
import {
  to = churchtools_relationship_type.couple
  id = "2"
}`},
			{Config: providerBlock(mock.URL) + `
resource "churchtools_relationship_type" "couple" {
  degree_name_a     = "relationship.part.spouse"
  degree_name_b     = "relationship.part.spouse"
  security_level_id = 3
}`},
		},
	})

	row := mock.Find("/person/relationshiptypes", "securityLevelId", float64(3))
	if row == nil {
		t.Fatal("update did not reach the mock")
	}
	if row["name"] != "relationship.couple" || row["exportTitle"] != "relationship.couple" || row["includeInExport"] != true {
		t.Errorf("update wiped unmanaged fields: %v", row)
	}
}

func TestAccRelationshipType_CreateWithoutIDFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.DropCreateID("/person/relationshiptypes")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_relationship_type" "x" {
  degree_name_a     = "Abholung durch"
  degree_name_b     = "Abholberechtigt für"
  security_level_id = 1
}`,
			ExpectError: regexp.MustCompile(`keine id zur`),
		}},
	})
}
