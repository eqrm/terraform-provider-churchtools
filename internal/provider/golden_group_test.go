package provider_test

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// attrName maps a state field to its attribute, passing unmapped names through
// as the exporter does.
func attrName(f string) string {
	if mapped, ok := tier0Attrs[f]; ok {
		return mapped
	}
	return f
}

// TestGolden_GroupNoOp is the group counterpart of the campus proof, with the
// two things that make groups different: CT serves the ids nested under
// `information` (so the mock is seeded in that shape, not the flat state shape),
// and the exporter OMITS null or absent fields, so an unset campus must plan
// empty against a campusId of JSON null.
//
// The fixture is synthetic: no real group, campus or person belongs in it.
func TestGolden_GroupNoOp(t *testing.T) {
	sf := loadFixture(t, "../../testdata/ct-state.synthetic.group.json")
	if len(sf.Resources) != 30 {
		t.Fatalf("fixture has %d groups, want 30", len(sf.Resources))
	}

	mock := testmock.New()
	defer mock.Close()

	keys := make([]string, 0, len(sf.Resources))
	for k := range sf.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var config, imports strings.Builder
	config.WriteString(providerBlock(mock.URL))
	sawZero, sawNull, sawAbsent := false, false, false
	for _, k := range keys {
		r := sf.Resources[k]
		if r.Type != "group" {
			t.Fatalf("fixture row %q has type %q, want group", k, r.Type)
		}
		campus, has := r.Fields["campusId"]
		switch {
		case !has:
			sawAbsent = true
		case campus == nil:
			sawNull = true
		case campus == float64(0):
			sawZero = true
		}

		// CT's shape: name top level, ids under information, campusId always
		// present (null when unset).
		mock.Seed("/groups", r.ID, map[string]any{
			"name": r.Fields["name"],
			"information": map[string]any{
				"groupTypeId":   r.Fields["groupTypeId"],
				"groupStatusId": r.Fields["groupStatusId"],
				"campusId":      campus,
			},
			// CT always serves settings.informLeader; the exporter output has no
			// inform_leader, so it must plan empty whichever value CT holds.
			"settings": map[string]any{"informLeader": r.ID%2 == 0},
		})

		fields := make([]string, 0, len(r.Fields))
		for f := range r.Fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)

		fmt.Fprintf(&config, "\nresource %q %q {\n", tier0HCLTypes[r.Type], hclLabel(r.Key))
		for _, f := range fields {
			if r.Fields[f] == nil {
				continue // the exporter omits null fields
			}
			fmt.Fprintf(&config, "  %s = %s\n", attrName(f), hclLiteral(r.Fields[f]))
		}
		config.WriteString("}\n")
		fmt.Fprintf(&imports, "\nimport {\n  to = %s.%s\n  id = %q\n}\n",
			tier0HCLTypes[r.Type], hclLabel(r.Key), strconv.Itoa(r.ID))
	}
	if !sawZero || !sawNull || !sawAbsent {
		t.Fatalf("fixture must cover campus 0 (%v), null (%v) and an absent key (%v)", sawZero, sawNull, sawAbsent)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: config.String() + imports.String(), ExpectNonEmptyPlan: false},
			{Config: config.String(), PlanOnly: true, ExpectNonEmptyPlan: false},
		},
	})
}
