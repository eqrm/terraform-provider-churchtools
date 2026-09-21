package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/provider"
	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func protoV6() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"churchtools": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
}

func providerBlock(host string) string {
	return fmt.Sprintf("provider \"churchtools\" {\n  host  = %q\n  token = \"tok\"\n}\n", host)
}

func TestAccCampus_CreateAndUpdate(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_campus" "neu" {
  name   = "Neustadt"
  shorty = "NS"
}`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_campus.neu", "name", "Neustadt"),
					resource.TestCheckResourceAttr("churchtools_campus.neu", "shorty", "NS"),
					resource.TestCheckResourceAttrSet("churchtools_campus.neu", "id"),
				),
			},
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_campus" "neu" {
  name   = "Neustadt West"
  shorty = "NSW"
}`,
				Check: resource.TestCheckResourceAttr("churchtools_campus.neu", "name", "Neustadt West"),
			},
		},
	})
}

// The Mainz campus is id 0 on prod. Any truthiness check on the id breaks
// import for exactly one campus, silently.
func TestAccCampus_ImportZeroID(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/campuses", 0, map[string]any{"name": "Mainz", "shorty": "MZ"})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(mock.URL) + `
resource "churchtools_campus" "mainz" {
  name   = "Mainz"
  shorty = "MZ"
}`,
				ResourceName:  "churchtools_campus.mainz",
				ImportState:   true,
				ImportStateId: "0",
				// ImportStateVerify would compare against a prior apply step;
				// there is none here on purpose — the point is importing a row
				// that already exists on the instance, as the migration does.
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("imported %d states, want 1", len(states))
					}
					s := states[0]
					if s.ID != "0" {
						return fmt.Errorf("imported id = %q, want \"0\"", s.ID)
					}
					if got := s.Attributes["name"]; got != "Mainz" {
						return fmt.Errorf("imported name = %q, want Mainz", got)
					}
					if got := s.Attributes["shorty"]; got != "MZ" {
						return fmt.Errorf("imported shorty = %q, want MZ", got)
					}
					return nil
				},
			},
		},
	})
}

// The mock must refuse a collection it does not know about. Without this the
// acceptance suite cannot catch a resource built on an endpoint that does not
// exist on a live ChurchTools instance.
func TestMockRejectsUnregisteredCollection(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	// /persons is outside this tool's mandate and will never be registered.
	// (This used to probe /api/departments, which is now a registered
	// READ-ONLY collection — see TestMockRefusesDepartmentWrites.)
	resp, err := http.Get(mock.URL + "/api/persons")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unregistered collection returned %d, want 404", resp.StatusCode)
	}
}

// ChurchTools serves no REST write for Bereiche — every write goes through the
// legacy master-data endpoint. The mock must refuse POST/PUT on /departments,
// or a resource built on the wrong verbs goes green in CI again.
func TestMockRefusesDepartmentWrites(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()

	for _, method := range []string{http.MethodPost, http.MethodPut} {
		req, err := http.NewRequest(method, mock.URL+"/api/departments/1", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /departments returned %d, want 405", method, resp.StatusCode)
		}
	}

	resp, err := http.Get(mock.URL + "/api/departments")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /departments returned %d, want 200 — reads are REST", resp.StatusCode)
	}
}

// sessionProviderBlock configures the provider the way a credential helper
// does: no token anywhere, just the session `ct auth token` emits.
func sessionProviderBlock(host, cookie, csrf string) string {
	return fmt.Sprintf("provider \"churchtools\" {\n  host           = %q\n"+
		"  session_cookie = %q\n  csrf_token     = %q\n}\n", host, cookie, csrf)
}

// The end-to-end proof of #179's other half: a full create/update round-trip
// with the permanent login token never present. The mock refuses anything not
// carrying the session, so a green run means the cookie really authenticated
// the reads AND the CSRF header really authenticated the writes.
func TestAccCampus_SessionAuth(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.RequireSession("sid=abc", "csrf-1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: sessionProviderBlock(mock.URL, "sid=abc", "csrf-1") + `
resource "churchtools_campus" "neu" {
  name   = "Neustadt"
  shorty = "NS"
}`,
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("churchtools_campus.neu", "name", "Neustadt"),
					resource.TestCheckResourceAttrSet("churchtools_campus.neu", "id"),
				),
			},
			{
				Config: sessionProviderBlock(mock.URL, "sid=abc", "csrf-1") + `
resource "churchtools_campus" "neu" {
  name   = "Neustadt West"
  shorty = "NSW"
}`,
				Check: resource.TestCheckResourceAttr("churchtools_campus.neu", "name", "Neustadt West"),
			},
		},
	})
}

// A session the instance no longer accepts must say what fixes it. The provider
// holds no token, so it cannot renew one — re-running is the remedy, and a
// generic "unauthorized" would send the operator looking at their credentials.
func TestAccCampus_ExpiredSessionExplainsItself(t *testing.T) {
	mock := testmock.New()
	defer mock.Close()
	mock.RequireSession("sid=fresh", "csrf-1")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6(),
		Steps: []resource.TestStep{
			{
				Config: sessionProviderBlock(mock.URL, "sid=stale", "csrf-1") + `
resource "churchtools_campus" "neu" {
  name   = "Neustadt"
  shorty = "NS"
}`,
				ExpectError: regexp.MustCompile(`re-run`),
			},
		},
	})
}
