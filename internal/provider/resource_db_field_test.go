package provider_test

import (
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// seedStatusField installs data field 32 (statusId) in the shape a live
// GET /dbfields/32 returns on eqrm-dev (CT 3.137): nested fieldCategory and
// fieldType objects, a @deprecated block and its aliases. Values are chosen so
// that no two carried-over keys share a value, which lets a test tell a copied
// key from a defaulted one.
func seedStatusField(mock *testmock.Server, isNewPersonField bool) {
	mock.Seed("/dbfields", 32, map[string]any{
		"name":             "glossary.person-status",
		"nameTranslated":   "Personenstatus",
		"shorty":           "glossary.person-status",
		"column":           "status_id",
		"key":              "statusId",
		"length":           float64(11),
		"fieldCategory":    map[string]any{"id": float64(3), "name": "categories", "internCode": "f_category", "table": "cdb_person"},
		"fieldType":        map[string]any{"id": float64(2), "name": "fieldtype.select", "internCode": "select", "sortKey": float64(50)},
		"isActive":         true,
		"useAsPlaceholder": false,
		"isNewPersonField": isNewPersonField,
		"lineEnding":       "<br/>",
		"securityLevel":    float64(3),
		"sortKey":          float64(1),
		"deleteOnArchive":  false,
		"isNullable":       false,
		"hideInFrontend":   false,
		"createdByChurch":  false,
		"@deprecated":      map[string]any{"nullable": "isNullable", "notConfigurable": "isNotConfigurable"},
		"nullable":         false,
	})
}

func dbFieldConfig(host string, isNewPersonField string) string {
	return providerBlock(host) + `
import {
  to = churchtools_db_field.status
  id = "32"
}
resource "churchtools_db_field" "status" {
  is_new_person_field = ` + isNewPersonField + `
}`
}

// Import reads the field's key and flag; re-planning the same config is empty.
func TestAccDBField_ImportReads(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedStatusField(mock, false)

	cfg := dbFieldConfig(mock.URL, "false")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_db_field.status", "id", "32"),
					resource.TestCheckResourceAttr("churchtools_db_field.status", "key", "statusId"),
					resource.TestCheckResourceAttr("churchtools_db_field.status", "is_new_person_field", "false"),
					// Left out of the config, is_active reads the instance's value.
					resource.TestCheckResourceAttr("churchtools_db_field.status", "is_active", "true"),
				),
			},
			{Config: cfg, PlanOnly: true},
		},
	})
	if got := mock.LastPut("/dbfields"); got != nil {
		t.Errorf("an import with no diff sent a PUT: %v", got)
	}
}

// ChurchTools has no PATCH on /dbfields and a partial PUT is a 400 (the mock
// rejects it the same way), so Update must send the spec's full request body —
// every key copied from a fresh GET, only isNewPersonField changed, and none of
// the read-only nested objects.
func TestAccDBField_UpdateSendsFullBodyChangingOnlyTheFlag(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedStatusField(mock, true)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: dbFieldConfig(mock.URL, "true")},
			{
				Config: dbFieldConfig(mock.URL, "false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_db_field.status", "is_new_person_field", "false"),
					resource.TestCheckResourceAttr("churchtools_db_field.status", "key", "statusId"),
				),
			},
			{Config: dbFieldConfig(mock.URL, "false"), PlanOnly: true},
		},
	})

	got := mock.LastPut("/dbfields")
	if got == nil {
		t.Fatal("no PUT reached /dbfields")
	}
	want := map[string]any{
		"id":               float64(32),
		"name":             "glossary.person-status",
		"shorty":           "glossary.person-status",
		"length":           float64(11),
		"lineEnding":       "<br/>",
		"securityLevel":    float64(3),
		"sortKey":          float64(1),
		"isActive":         true,
		"useAsPlaceholder": false,
		"isNewPersonField": false,
		"deleteOnArchive":  false,
	}
	if !reflect.DeepEqual(got, want) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("PUT body = %v (keys %v)\nwant       %v", got, keys, want)
	}
	row := mock.Find("/dbfields", "key", "statusId")
	if row == nil || row["isNewPersonField"] != false {
		t.Fatalf("flag did not land on the mock: %v", row)
	}
}

// seedJobField installs data field 19 (job, "Beruf") as eqrm-dev returns it — the
// shape IT-29 switches off. Trimmed to the keys Update reads plus one nested
// read-only object, which must not reach the PUT.
func seedJobField(mock *testmock.Server) {
	mock.Seed("/dbfields", 19, map[string]any{
		"name":             "profession",
		"shorty":           "profession",
		"key":              "job",
		"length":           float64(50),
		"fieldCategory":    map[string]any{"id": float64(2), "name": "information"},
		"isActive":         true,
		"useAsPlaceholder": false,
		"isNewPersonField": false,
		"lineEnding":       "<br/>",
		"securityLevel":    float64(3),
		"sortKey":          float64(4),
		"deleteOnArchive":  false,
	})
}

func jobFieldConfig(host, isActive string) string {
	attr := ""
	if isActive != "" {
		attr = "\n  is_active = " + isActive
	}
	return providerBlock(host) + `
import {
  to = churchtools_db_field.job
  id = "19"
}
resource "churchtools_db_field" "job" {
  is_new_person_field = false` + attr + `
}`
}

// IT-29: importing a field with is_active = false switches it off in the same
// apply, through the full PUT body, and leaves isNewPersonField alone.
func TestAccDBField_ImportDeactivates(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedJobField(mock)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: jobFieldConfig(mock.URL, "false"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_db_field.job", "key", "job"),
					resource.TestCheckResourceAttr("churchtools_db_field.job", "is_active", "false"),
				),
			},
			{Config: jobFieldConfig(mock.URL, "false"), PlanOnly: true},
		},
	})

	want := map[string]any{
		"id":               float64(19),
		"name":             "profession",
		"shorty":           "profession",
		"length":           float64(50),
		"lineEnding":       "<br/>",
		"securityLevel":    float64(3),
		"sortKey":          float64(4),
		"isActive":         false,
		"useAsPlaceholder": false,
		"isNewPersonField": false,
		"deleteOnArchive":  false,
	}
	if got := mock.LastPut("/dbfields"); !reflect.DeepEqual(got, want) {
		t.Errorf("PUT body = %v\nwant       %v", got, want)
	}
	if row := mock.Find("/dbfields", "key", "job"); row == nil || row["isActive"] != false {
		t.Fatalf("isActive did not land on the mock: %v", row)
	}
}

// Dropping is_active from the config after managing it keeps the last value
// (Optional+Computed): no plan, no PUT that would silently re-activate.
func TestAccDBField_OmittedIsActiveKeepsState(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedJobField(mock)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: jobFieldConfig(mock.URL, "false")},
			{Config: jobFieldConfig(mock.URL, ""), PlanOnly: true},
		},
	})
}

// Data fields are never created by this provider: Create refuses and points at
// import, instead of POSTing a custom field.
func TestAccDBField_CreateRefusesAndPointsAtImport(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_db_field" "status" {
  is_new_person_field = false
}`,
			ExpectError: regexp.MustCompile(`(?s)kann nicht angelegt werden.*import`),
		}},
	})
}

// resource.Test destroys what it applied. Delete must un-manage only: field 32
// is still on the instance afterwards, unchanged. The mock serves DELETE on
// /dbfields because a live instance does, so a real DELETE would remove it.
func TestAccDBField_DestroyOrphans(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	seedStatusField(mock, false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps:                    []resource.TestStep{{Config: dbFieldConfig(mock.URL, "false")}},
	})

	row := mock.Find("/dbfields", "key", "statusId")
	if row == nil {
		t.Fatal("destroy deleted data field 32 from ChurchTools; Delete must only un-manage")
	}
	if row["isNewPersonField"] != false {
		t.Errorf("destroy changed isNewPersonField to %v", row["isNewPersonField"])
	}
}
