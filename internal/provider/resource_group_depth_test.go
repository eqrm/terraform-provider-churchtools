package provider_test

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const depthGroupID = 1001 // the mock's first create id

// subgroup_permission_depth lives on the legacy churchauth ajax. Unset it is not managed:
// no getMasterData (a ~4 MB answer on a real host), no updateDeepNo, no diff, null in state.
func TestAccGroupDepth_UnsetIsUntouched(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	cfg := groupConfig(mock.URL, "Gruppe D0", ``)
	untouched := func(*terraform.State) error {
		if r, w := mock.DepthReads(), mock.DepthWrites(); r != 0 || w != 0 {
			t.Errorf("unset depth made %d churchauth reads and %d writes, want none", r, w)
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckNoResourceAttr("churchtools_group.g", "subgroup_permission_depth"), untouched)},
			{Config: cfg, PlanOnly: true},
			{Config: groupConfig(mock.URL, "Gruppe D0 neu", ``), Check: untouched}, // an unrelated PATCH
			{ResourceName: "churchtools_group.g", ImportState: true, ImportStateVerify: true},
		},
	})
}

// Set: written on create (after the POST), read back, drift detected, changed by update.
func TestAccGroupDepth_Lifecycle(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	const addr = "churchtools_group.g"
	cfg := func(v string) string {
		return groupConfig(mock.URL, "Gruppe D1", "  subgroup_permission_depth = "+v)
	}
	held := func(want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := mock.GroupDepth(depthGroupID); got != want {
				t.Errorf("CT holds permission_deep_no = %q, want %q", got, want)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg("7"), ExpectError: regexp.MustCompile(`(?s)Ungültiger Wert.*0, 1, 2, 3, 999`)},
			{
				Config: cfg("1"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "subgroup_permission_depth", "1"), held("1"),
					func(*terraform.State) error {
						want := map[string]string{"domain_type": "groupMemberstatus", "domain_id": "1001", "deep_no": "1"}
						if got := mock.LastDepthWrite(); !reflect.DeepEqual(got, want) {
							t.Errorf("updateDeepNo form = %v, want %v", got, want)
						}
						return nil
					}),
			},
			{Config: cfg("1"), PlanOnly: true},
			{Config: cfg("2"), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(addr, "subgroup_permission_depth", "2"), held("2"))},
			{
				// Someone moves it in the UI: detected, then put back.
				PreConfig:          func() { mock.SetGroupDepth(depthGroupID, "3") },
				Config:             cfg("2"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: cfg("2"), Check: held("2")},
			{
				// 999 is "Alle Ebenen" and CT stores it as the string "999".
				Config: cfg("999"),
				Check:  resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr(addr, "subgroup_permission_depth", "999"), held("999")),
			},
			{
				// Unset again: the provider lets go, nothing is written, the state value stays.
				Config:   groupConfig(mock.URL, "Gruppe D1", ``),
				PlanOnly: true,
			},
		},
	})
}

// CT answers success to a write it did not store (and to unknown groups): the read-back
// turns that into an error instead of a state that lies.
func TestAccGroupDepth_WriteNotAppliedFails(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.IgnoreDeepNoWrites()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config:      groupConfig(mock.URL, "Gruppe D2", `  subgroup_permission_depth = 1`),
			ExpectError: regexp.MustCompile(`(?s)Gruppe wurde angelegt, subgroup_permission_depth aber nicht gesetzt.*steht\s+danach aber auf 0`),
		}},
	})
}

// An import leaves the depth null (config is not visible to Read); with the attribute
// set, the next apply writes it and reads it back.
func TestAccGroupDepth_ImportThenSet(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/groups", 5003, map[string]any{
		"name":        "Gruppe D3",
		"information": map[string]any{"groupTypeId": float64(11), "groupStatusId": float64(2), "campusId": nil},
		"settings":    map[string]any{"informLeader": false, "visibility": "restricted"},
	})
	mock.SetGroupDepth(5003, "1")
	cfg := providerBlock(mock.URL) + `
import {
  to = churchtools_group.g
  id = "5003"
}
resource "churchtools_group" "g" {
  name            = "Gruppe D3"
  group_type_id   = "11"
  group_status_id = "2"
  subgroup_permission_depth = 1
}`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.TestCheckResourceAttr("churchtools_group.g", "subgroup_permission_depth", "1")},
			{Config: cfg, PlanOnly: true},
		},
	})
}
