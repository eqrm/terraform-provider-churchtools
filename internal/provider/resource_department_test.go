package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// A Bereich create must go through the LEGACY master-data endpoint and then
// learn its id by diffing GET /departments, because saveMasterData returns no
// id. If this ever regresses to POST /departments, the mock answers 405.
func TestAccDepartment_CreatesViaLegacyEndpoint(t *testing.T) {
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
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("churchtools_department.musik", "name", "Bereich Musik"),
				resource.TestCheckResourceAttr("churchtools_department.musik", "shorty", "MU"),
				// ct-cli stores sortKey as `?? 0`; matching it keeps an export
				// that omits the field a no-op instead of a diff.
				resource.TestCheckResourceAttr("churchtools_department.musik", "sort_key", "0"),
				resource.TestCheckResourceAttrSet("churchtools_department.musik", "id"),
			),
		}},
	})

	if row := mock.Find("/departments", "name", "Bereich Musik"); row == nil {
		t.Fatal("the Bereich never reached /departments — the legacy write did not land")
	}
}

func TestAccDepartment_UpdatesByID(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name     = "Bereich Musik"
  shorty   = "MU"
  sort_key = 3
}`,
			},
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name     = "Bereich Musik und Technik"
  shorty   = "MT"
  sort_key = 4
}`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_department.musik", "name", "Bereich Musik und Technik"),
					resource.TestCheckResourceAttr("churchtools_department.musik", "sort_key", "4"),
				),
			},
		},
	})

	if mock.Find("/departments", "name", "Bereich Musik") != nil {
		t.Error("the old row survived — the update created a second Bereich instead of updating by id")
	}
}

// The legacy endpoint returns no id, so a second Bereich of the same name could
// not be told from the first — and the create would already have succeeded,
// leaving an orphan that every retry duplicates. Refuse before writing.
func TestAccDepartment_RefusesDuplicateName(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/departments", 3, map[string]any{"name": "Bereich Musik", "shorty": "MU", "sortKey": float64(0)})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name   = "Bereich Musik"
  shorty = "MU"
}`,
			ExpectError: regexp.MustCompile(`Bereich existiert bereits`),
		}},
	})
}

// The duplicate-name guard filters GET /departments, which CT pages. If List
// stops at page 1 the guard never sees a Bereich that sits further down, passes,
// and SaveMasterData writes a SECOND Bereich with the same name -- the exact
// corruption the guard exists to prevent. Seed well past one page and put the
// clashing name last.
func TestAccDepartment_RefusesDuplicateBeyondFirstPage(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	for i := 1; i <= 250; i++ {
		mock.Seed("/departments", i, map[string]any{
			"name": fmt.Sprintf("Bereich %d", i), "shorty": "B", "sortKey": float64(0),
		})
	}
	mock.Seed("/departments", 900, map[string]any{"name": "Bereich Technik", "shorty": "TE", "sortKey": float64(0)})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_department" "technik" {
  name   = "Bereich Technik"
  shorty = "TE"
}`,
			ExpectError: regexp.MustCompile(`Bereich existiert bereits`),
		}},
	})
}

// The bug this whole resource was rewritten for: Read used GET /departments/{id},
// which does not exist on a live instance. The mock now refuses it, so a
// regression back to an item read fails here instead of on a customer's server.
func TestMockRefusesDepartmentItemRead(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/departments", 3, map[string]any{"name": "Bereich Musik"})

	resp, err := http.Get(mock.URL + "/api/departments/3")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /departments/3 returned %d, want 404", resp.StatusCode)
	}

	// The collection read must still work — it is the only read path.
	list, err := http.Get(mock.URL + "/api/departments")
	if err != nil {
		t.Fatalf("GET collection: %v", err)
	}
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Errorf("GET /departments returned %d, want 200", list.StatusCode)
	}
}

// POST /departments must be refused: Bereiche have no REST write path.
func TestMockRefusesDepartmentRestWrite(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resp, err := http.Post(mock.URL+"/api/departments", "application/json", strings.NewReader(`{"name":"X"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /departments returned %d, want 405", resp.StatusCode)
	}
}

// A config that omits sort_key must not rewrite an existing row's real value.
// With a StaticInt64(0) default, importing a Bereich whose sortKey is 30 and
// leaving sort_key out of the HCL rewrote it to 0 on the next apply, reordering
// the Bereich list instance-wide.
func TestAccDepartment_ImportKeepsSortKeyWhenOmitted(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/departments", 3, map[string]any{"name": "Bereich Musik", "shorty": "MU", "sortKey": float64(30)})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: providerBlock(mock.URL) + `
resource "churchtools_department" "musik" {
  name   = "Bereich Musik"
  shorty = "MU"
}`,
			ResourceName:  "churchtools_department.musik",
			ImportState:   true,
			ImportStateId: "3",
			ImportStateCheck: func(states []*terraform.InstanceState) error {
				if len(states) != 1 {
					return fmt.Errorf("imported %d states, want 1", len(states))
				}
				if got := states[0].Attributes["sort_key"]; got != "30" {
					return fmt.Errorf("sort_key = %q after import, want 30", got)
				}
				return nil
			},
		}},
	})
}

// Departments are the ONLY resource written through the legacy /index.php
// endpoint, so session auth reaching REST proves nothing about them. The mock
// holds that path to the same session as every REST route, which means a
// create landing in /departments is evidence the cookie and the CSRF token
// were both carried on the form-encoded write.
func TestAccDepartment_SessionAuthOnTheLegacyEndpoint(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.RequireSession("sid=abc", "csrf-1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{{
			Config: sessionProviderBlock(mock.URL, "sid=abc", "csrf-1") + `
resource "churchtools_department" "musik" {
  name   = "Bereich Musik"
  shorty = "MU"
}`,
			Check: resource.TestCheckResourceAttrSet("churchtools_department.musik", "id"),
		}},
	})

	if row := mock.Find("/departments", "name", "Bereich Musik"); row == nil {
		t.Fatal("the Bereich never reached /departments — the legacy write did not carry the session")
	}
}

// The gate that makes the test above mean something. Without RequireSession
// covering /index.php, handleLegacy asks only that the two headers be
// NON-EMPTY -- so a stale cookie or a wrong CSRF token sails through and a
// session-mode department test goes green while proving nothing. This asserts
// the mock discriminates, which is the property the test above leans on.
func TestMockHoldsTheLegacyEndpointToTheSession(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.RequireSession("sid=fresh", "csrf-1")

	for _, tc := range []struct{ name, cookie, csrf string }{
		{"stale cookie", "sid=stale", "csrf-1"},
		{"wrong csrf", "sid=fresh", "csrf-wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, mock.URL+"/index.php?q=churchdb/ajax",
				strings.NewReader("func=saveMasterData&table=cdb_bereich"))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Cookie", tc.cookie)
			req.Header.Set("CSRF-Token", tc.csrf)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("legacy POST with a %s returned %d, want 401", tc.name, resp.StatusCode)
			}
		})
	}
}

// Same snapshot→write→diff as privacy_agreement_type, same parallel hazard.
func TestAccDepartment_ParallelCreatesEachFindTheirRow(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	config := providerBlock(mock.URL)
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		config += `
resource "churchtools_department" "` + n + `" {
  name   = "Bereich ` + n + `"
  shorty = "` + n + `"
}`
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps:                    []resource.TestStep{{Config: config}},
	})
}
