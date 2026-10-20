package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
)

// Update re-runs planGroupTypeMigration with ModifyPlan bypassed, so a member who
// joined an empty role between plan and apply must still stop the migration.
func TestPlanGroupTypeMigration_RechecksAtApply(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/groups", 5001, map[string]any{
		"name":        "G",
		"information": map[string]any{"groupTypeId": float64(11)},
	})
	role := func(id, typ int, name, kind string, def bool) {
		mock.Seed("/group/roles", id, map[string]any{"name": name, "groupTypeId": float64(typ), "type": kind, "isDefault": def})
	}
	role(101, 11, "Leiter", "leader", false)
	role(103, 11, "Gast", "participant", false)
	role(201, 12, "Leiter", "leader", false)
	role(202, 12, "Teilnehmer", "participant", true)
	mock.SeedMembers(5001, 101)

	c := client.New(mock.URL, "tok")
	mig, err := planGroupTypeMigration(context.Background(), c, "5001", 11, 12)
	if err != nil {
		t.Fatalf("plan with an empty Gast role: %v", err)
	}
	if got := mig.describe(); !strings.Contains(got, `"Gast" -> "Teilnehmer" (reason: empty-role)`) ||
		!strings.Contains(got, `"Leiter" -> "Leiter" (reason: name)`) {
		t.Errorf("describe() = %q", got)
	}

	mock.SeedMembers(5001, 101, 103) // someone joins Gast after the plan
	if _, err := planGroupTypeMigration(context.Background(), c, "5001", 11, 12); err == nil ||
		!strings.Contains(err.Error(), `"Gast" (Rolle 103, 1 Mitglied`) {
		t.Errorf("apply-time re-check = %v, want a refusal naming Gast", err)
	}
}

// A same-name match can turn a leader into a participant; the mapping stays, but the
// warning marks the move.
func TestDescribe_MarksKindChange(t *testing.T) {
	m := groupTypeMigration{moves: []roleMove{
		{from: typeRole{name: "Leiter", kind: "leader"}, to: typeRole{name: "Leiter", kind: "participant"}, reason: "name"},
		{from: typeRole{name: "Gast", kind: "participant"}, to: typeRole{name: "Gast", kind: "participant"}, reason: "name"},
	}}
	got := m.describe()
	if !strings.Contains(got, `"Leiter" -> "Leiter" (reason: name) (Art wechselt: leader -> participant)`) {
		t.Errorf("describe() = %q, want the kind change marked", got)
	}
	if strings.Count(got, "Art wechselt") != 1 {
		t.Errorf("describe() = %q, want exactly one kind change", got)
	}
}

// The member read asks for every status explicitly.
func TestPlanGroupTypeMigration_RequestsAllMemberStatuses(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/groups", 5001, map[string]any{"name": "G", "information": map[string]any{"groupTypeId": float64(11)}})
	mock.Seed("/group/roles", 101, map[string]any{"name": "A", "groupTypeId": float64(11), "type": "participant", "isDefault": true})
	mock.Seed("/group/roles", 201, map[string]any{"name": "A", "groupTypeId": float64(12), "type": "participant", "isDefault": true})
	mock.SeedMembers(5001, 101)
	if _, err := planGroupTypeMigration(context.Background(), client.New(mock.URL, "tok"), "5001", 11, 12); err != nil {
		t.Fatal(err)
	}
	want := []string{"active", "requested", "to_delete", "waiting"}
	if got := mock.LastMembersStatuses(5001); !reflect.DeepEqual(got, want) {
		t.Errorf("group_member_statuses[] = %v, want %v", got, want)
	}
}

// A null or unknown group_type_id must not become type 0.
func TestPlanTypeChange_RefusesNonNumericIDs(t *testing.T) {
	r := &groupResource{}
	for name, c := range map[string]struct{ from, to types.String }{
		"null from":    {types.StringNull(), types.StringValue("12")},
		"null to":      {types.StringValue("11"), types.StringNull()},
		"unknown to":   {types.StringValue("11"), types.StringUnknown()},
		"unknown from": {types.StringUnknown(), types.StringValue("12")},
	} {
		_, err := r.planTypeChange(context.Background(), "5001", c.from, c.to)
		if err == nil || !strings.Contains(err.Error(), "group_type_id") {
			t.Errorf("%s: err = %v, want a group_type_id error", name, err)
		}
	}
}
