package provider_test

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func groupConfig(host, name, campusLine string) string {
	return providerBlock(host) + `
resource "churchtools_group" "g" {
  name            = "` + name + `"
  group_type_id   = "11"
  group_status_id = "2"
` + campusLine + `
}`
}

// The whole write lifecycle in one run: create, import, a name-only PATCH, campus
// "0" (a real campus, must not read as unset), clearing the campus (an explicit
// null on the wire and null in state), and a destroy that leaves the row.
func TestAccGroup_Lifecycle(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	const addr = "churchtools_group.g"

	patchIs := func(want map[string]any) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := mock.LastPatch("/groups"); !reflect.DeepEqual(got, want) {
				t.Errorf("PATCH body = %v, want %v", got, want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: groupConfig(mock.URL, "Gruppe 001", `  campus_id       = "1"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "id"),
					resource.TestCheckResourceAttr(addr, "name", "Gruppe 001"),
					resource.TestCheckResourceAttr(addr, "group_type_id", "11"),
					resource.TestCheckResourceAttr(addr, "group_status_id", "2"),
					resource.TestCheckResourceAttr(addr, "campus_id", "1"),
					func(*terraform.State) error {
						// Ints on the wire, as CT's POST takes them.
						row := mock.Find("/groups", "name", "Gruppe 001")
						if row == nil {
							t.Fatal("created group not in mock")
						}
						want := map[string]any{"groupTypeId": float64(11), "groupStatusId": float64(2), "campusId": float64(1)}
						if info := row["information"].(map[string]any); !reflect.DeepEqual(info, want) {
							t.Errorf("stored information = %v, want %v", info, want)
						}
						return nil
					},
				),
			},
			{ResourceName: addr, ImportState: true, ImportStateVerify: true},
			{
				Config: groupConfig(mock.URL, "Gruppe 001 neu", `  campus_id       = "1"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", "Gruppe 001 neu"),
					patchIs(map[string]any{"name": "Gruppe 001 neu"}),
				),
			},
			{
				Config: groupConfig(mock.URL, "Gruppe 001 neu", `  campus_id       = "0"`),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "campus_id", "0"),
					patchIs(map[string]any{"campusId": float64(0)}),
				),
			},
			{
				Config: groupConfig(mock.URL, "Gruppe 001 neu", ``),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "campus_id"),
					patchIs(map[string]any{"campusId": nil}),
				),
			},
			{
				// Still empty on re-plan: null campus in CT equals an omitted attribute.
				Config:   groupConfig(mock.URL, "Gruppe 001 neu", ``),
				PlanOnly: true,
			},
		},
	})

	// The framework destroyed the resource at the end; the group must live on.
	if mock.Find("/groups", "name", "Gruppe 001 neu") == nil {
		t.Error("destroy removed the group from ChurchTools; Delete must only un-manage")
	}
}

// A group deleted in the UI is dropped from state by Read, so the next plan
// recreates it instead of erroring.
func TestAccGroup_DeletedOutOfBandIsRecreated(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	cfg := groupConfig(mock.URL, "Gruppe 002", ``)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					row := mock.Find("/groups", "name", "Gruppe 002")
					mock.Remove("/groups", int(row["id"].(float64)))
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg}, // apply recreates
		},
	})
}

// CT answering 200 and keeping the old value must fail the apply, not be stored.
func TestAccGroup_IgnoredPatchFieldFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/groups", 5001, map[string]any{
		"name": "Gruppe 003",
		"information": map[string]any{
			"groupTypeId": float64(11), "groupStatusId": float64(2), "campusId": nil,
		},
	})
	mock.IgnoreOnPatch("/groups", 5001, "campusId")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(mock.URL) + `
import {
  to = churchtools_group.g
  id = "5001"
}
resource "churchtools_group" "g" {
  name            = "Gruppe 003"
  group_type_id   = "11"
  group_status_id = "2"
}`,
			},
			{
				Config:      groupConfig(mock.URL, "Gruppe 003", `  campus_id       = "1"`),
				ExpectError: regexp.MustCompile(`(?s)ChurchTools hat die.*ignoriert`),
			},
		},
	})
}

func TestAccGroup_CreateWithoutIDFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.DropCreateID("/groups")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config:      groupConfig(mock.URL, "Gruppe 004", ``),
				ExpectError: regexp.MustCompile(`(?s)keine\s+id`),
			},
		},
	})
}

func TestAccGroup_ImportRejectsNonNumericID(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config:        groupConfig(mock.URL, "Gruppe 005", ``),
				ResourceName:  "churchtools_group.g",
				ImportState:   true,
				ImportStateId: "Gruppe 005",
				ExpectError:   regexp.MustCompile(`(?s)keine\s+numerische`),
			},
		},
	})
}
