package provider_test

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// stateFile is the shape of ct-cli's ct-state.<env>.json, which `ct export tf`
// reads to produce HCL. The golden test consumes the SAME shape so the two
// sides cannot drift.
type stateFile struct {
	Resources map[string]struct {
		Type   string         `json:"type"`
		ID     int            `json:"id"`
		Key    string         `json:"key"`
		Fields map[string]any `json:"fields"`
	} `json:"resources"`
}

func loadFixture(t *testing.T, path string) stateFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return sf
}

// hclLiteral mirrors the exporter's value rendering in ct-cli.
func hclLiteral(v any) string {
	switch t := v.(type) {
	case string:
		return strconv.Quote(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return strconv.Quote(fmt.Sprint(t))
	}
}

func cloneRow(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// TestGolden_CampusNoOp is the executable form of "the export is faithful":
// seed a mock from the same state shape the exporter reads, import every row,
// then re-plan. A dropped field, a mangled id, or an attribute that reads back
// differently than it was written all surface here as a non-empty plan — the
// perpetual-diff failure mode, caught before it reaches a real instance.
//
// The fixture is SYNTHETIC. This repo becomes public at tier-1 completion, so
// no live campus name belongs in it. The proof against the real estate is the
// zero-change `tofu plan` run in the private config repo during migration.
func TestGolden_CampusNoOp(t *testing.T) {
	sf := loadFixture(t, "../../testdata/ct-state.synthetic.campus.json")
	if len(sf.Resources) != 15 {
		t.Fatalf("fixture has %d campuses, want 15", len(sf.Resources))
	}

	mock := testmock.New()
	defer mock.Close()

	keys := make([]string, 0, len(sf.Resources))
	for k := range sf.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic config text

	var config, imports strings.Builder
	config.WriteString(providerBlock(mock.URL))
	sawZero := false
	for _, k := range keys {
		r := sf.Resources[k]
		if r.ID == 0 {
			sawZero = true
		}
		mock.Seed("/campuses", r.ID, cloneRow(r.Fields))
		fmt.Fprintf(&config, "\nresource \"churchtools_campus\" %q {\n  name   = %s\n  shorty = %s\n}\n",
			r.Key, hclLiteral(r.Fields["name"]), hclLiteral(r.Fields["shorty"]))
		fmt.Fprintf(&imports, "\nimport {\n  to = churchtools_campus.%s\n  id = %q\n}\n",
			r.Key, strconv.Itoa(r.ID))
	}
	if !sawZero {
		t.Fatal("fixture must include a row with id 0 — that case is the whole point")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				// Import every row. Nothing may be created or changed.
				Config:             config.String() + imports.String(),
				ExpectNonEmptyPlan: false,
			},
			{
				// Re-plan without the import blocks: still empty.
				Config:             config.String(),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

var tier0Collections = map[string]string{
	"campus":         "/campuses",
	"group-type":     "/group/grouptypes",
	"department":     "/departments",
	"person-status":  "/statuses",
	"comment-viewer": "/person/commentviewers",
}

var tier0HCLTypes = map[string]string{
	"campus":         "churchtools_campus",
	"group-type":     "churchtools_group_type",
	"department":     "churchtools_department",
	"person-status":  "churchtools_person_status",
	"comment-viewer": "churchtools_comment_viewer",
}

// tier0Attrs maps CT's camelCase state fields onto the provider's snake_case
// attributes. It must stay identical to HCL_ATTR in ct-cli's src/export/hcl.ts;
// a divergence here is exactly the bug this test exists to catch.
var tier0Attrs = map[string]string{
	"nameTranslated":  "name_translated",
	"isMember":        "is_member",
	"isSearchable":    "is_searchable",
	"sortKey":         "sort_key",
	"securityLevelId": "security_level_id",
}

// hclLabel mirrors the exporter: a reference must be a valid identifier, and a
// key like "3_aktiv" (from "!3 Aktiv") is not one.
func hclLabel(key string) string {
	safe := []rune(key)
	for i, c := range safe {
		isOK := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
		if !isOK {
			safe[i] = '_'
		}
	}
	out := string(safe)
	if len(out) > 0 && (out[0] == '-' || (out[0] >= '0' && out[0] <= '9')) {
		return "g_" + out
	}
	return out
}

// TestGolden_Tier0NoOp widens the campus proof to every tier-0 type at the
// real estate's row distribution (15/13/10/7/5 = 50), including a key that has
// to be relabelled to be referenceable.
func TestGolden_Tier0NoOp(t *testing.T) {
	sf := loadFixture(t, "../../testdata/ct-state.synthetic.tier0.json")
	if len(sf.Resources) != 50 {
		t.Fatalf("fixture has %d rows, want 50", len(sf.Resources))
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
	relabelled := 0
	for _, k := range keys {
		r := sf.Resources[k]
		collection, ok := tier0Collections[r.Type]
		if !ok {
			t.Fatalf("fixture row %q has unmapped type %q", k, r.Type)
		}
		mock.Seed(collection, r.ID, cloneRow(r.Fields))

		label := hclLabel(r.Key)
		if label != r.Key {
			relabelled++
		}

		// Deterministic attribute order so the config text is stable.
		fields := make([]string, 0, len(r.Fields))
		for f := range r.Fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)

		fmt.Fprintf(&config, "\nresource %q %q {\n", tier0HCLTypes[r.Type], label)
		for _, f := range fields {
			name := f
			if mapped, ok := tier0Attrs[f]; ok {
				name = mapped
			}
			fmt.Fprintf(&config, "  %s = %s\n", name, hclLiteral(r.Fields[f]))
		}
		config.WriteString("}\n")

		fmt.Fprintf(&imports, "\nimport {\n  to = %s.%s\n  id = %q\n}\n",
			tier0HCLTypes[r.Type], label, strconv.Itoa(r.ID))
	}

	if relabelled == 0 {
		t.Fatal("fixture must include a key that needs relabelling — that case is the whole point")
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{Config: config.String() + imports.String(), ExpectNonEmptyPlan: false},
			{Config: config.String(), PlanOnly: true, ExpectNonEmptyPlan: false},
		},
	})
}
