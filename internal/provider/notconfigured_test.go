package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// nullStateFor builds an all-null object of the resource's own schema, with the
// id set so a Read/Update reaches the point where it would use the client.
// Null is what the framework hands a resource whose plan was never resolved,
// which is the situation being reproduced.
func nullStateFor(ctx context.Context, t *testing.T, r resource.Resource) (rschema.Schema, tftypes.Value) {
	t.Helper()

	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	if sresp.Diagnostics.HasError() {
		t.Fatalf("Schema: %v", sresp.Diagnostics)
	}

	obj, ok := sresp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("schema type is not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, typ := range obj.AttributeTypes {
		if name == "id" {
			// tftypes.NewValue PANICS on a type mismatch, which would abort the
			// whole binary with a tftypes stack trace in exactly the case this
			// test exists to catch: a resource added later. sort_key and
			// security_level_id are already Int64Attribute, so an id declared
			// that way is a plausible mistake -- make it a readable failure.
			if !typ.Is(tftypes.String) {
				t.Fatalf("%T declares a non-string id (%s); teach nullStateFor to build one", r, typ)
			}
			vals[name] = tftypes.NewValue(typ, "1")
			continue
		}
		vals[name] = tftypes.NewValue(typ, nil)
	}
	return sresp.Schema, tftypes.NewValue(obj, vals)
}

// A resource can reach CRUD holding no client: the provider's Configure DEFERS
// while any credential is unknown -- the normal state for session_cookie and
// csrf_token, which arrive from a `data "external"` block -- and sets no
// ResourceData, so configureClient hands back the nil it was given.
//
// The CRASH is already prevented one layer down: client.do and client.AjaxJSON
// refuse a nil receiver, so without this guard the operator gets
//
//	Standort konnte nicht angelegt werden: churchtools: client not configured
//	(provider credentials were still unknown): POST /campuses
//
// -- which blames the resource for a provider problem, leaks the transport's
// English internals into a German diagnostic, and names no remedy. What this
// pins is the diagnosis, not the survival: the failure belongs to the provider
// and must say what to do about it. Removing BOTH layers is what panics; that
// is why both exist.
//
// Delete and ImportState are excluded because neither touches the client.
func TestCRUD_WithoutAClientNamesTheProviderNotTheResource(t *testing.T) {
	ctx := context.Background()
	p := New("test")().(*churchtoolsProvider)

	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		var mresp resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "churchtools"}, &mresp)
		name := mresp.TypeName

		sch, raw := nullStateFor(ctx, t, r)
		plan := tfsdk.Plan{Schema: sch, Raw: raw}
		state := tfsdk.State{Schema: sch, Raw: raw}

		// Configure is deliberately NOT called with provider data here -- that is
		// precisely the deferred-provider case.
		t.Run(name+"/Create", func(t *testing.T) {
			var resp resource.CreateResponse
			resp.State = state
			r.Create(ctx, resource.CreateRequest{Plan: plan}, &resp)
			assertNotConfigured(t, resp.Diagnostics.Errors())
		})

		t.Run(name+"/Read", func(t *testing.T) {
			var resp resource.ReadResponse
			resp.State = state
			r.Read(ctx, resource.ReadRequest{State: state}, &resp)
			assertNotConfigured(t, resp.Diagnostics.Errors())
		})

		t.Run(name+"/Update", func(t *testing.T) {
			var resp resource.UpdateResponse
			resp.State = state
			r.Update(ctx, resource.UpdateRequest{Plan: plan, State: state}, &resp)
			assertNotConfigured(t, resp.Diagnostics.Errors())
		})
	}
}

func assertNotConfigured(t *testing.T, errs []diag.Diagnostic) {
	t.Helper()
	for _, e := range errs {
		if e.Summary() == notConfiguredSummary {
			if !strings.Contains(e.Detail(), "unbekannt") {
				t.Errorf("detail %q does not explain the deferred credential", e.Detail())
			}
			return
		}
	}
	t.Errorf("no %q diagnostic; got %v", notConfiguredSummary, errs)
}
