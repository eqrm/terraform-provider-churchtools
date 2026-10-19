package provider

import (
	"context"
	"strings"
	"testing"

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
