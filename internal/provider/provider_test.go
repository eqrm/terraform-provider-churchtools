package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestProvider_Schema(t *testing.T) {
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	for _, name := range []string{"host", "token", "session_cookie", "csrf_token"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing provider attribute %q", name)
		}
	}
	for _, name := range []string{"token", "session_cookie", "csrf_token"} {
		if !resp.Schema.Attributes[name].IsSensitive() {
			t.Errorf("%s attribute must be marked sensitive", name)
		}
	}
}

// A scheme-less or empty host must be rejected at Configure time. Without this
// the mistake surfaces much later as `unsupported protocol scheme ""` from
// inside net/http, which reads like a provider bug rather than a config typo.
func TestValidateHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"https ok", "https://example.church.tools", false},
		{"http ok", "http://localhost:8080", false},
		{"trailing slash ok", "https://example.church.tools/", false},
		{"empty", "", true},
		{"blank", "   ", true},
		{"no scheme", "example.church.tools", true},
		{"scheme only", "https://", true},
		{"wrong scheme", "ftp://example.church.tools", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateHost(tc.host)
			if gotErr := msg != ""; gotErr != tc.wantErr {
				t.Errorf("validateHost(%q) = %q; wantErr=%v", tc.host, msg, tc.wantErr)
			}
		})
	}
}

// configureWith runs the provider's Configure against a literal config, which
// is the only place the credential rules are enforced.
func configureWith(t *testing.T, host, token, cookie, csrf string) (*ProviderData, diag.Diagnostics) {
	t.Helper()
	p := New("test")()

	var sresp provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &sresp)

	attrTypes := map[string]tftypes.Type{
		"host":           tftypes.String,
		"token":          tftypes.String,
		"session_cookie": tftypes.String,
		"csrf_token":     tftypes.String,
	}
	raw := tftypes.NewValue(tftypes.Object{AttributeTypes: attrTypes}, map[string]tftypes.Value{
		"host":           tftypes.NewValue(tftypes.String, host),
		"token":          tftypes.NewValue(tftypes.String, token),
		"session_cookie": tftypes.NewValue(tftypes.String, cookie),
		"csrf_token":     tftypes.NewValue(tftypes.String, csrf),
	})

	var resp provider.ConfigureResponse
	p.Configure(context.Background(),
		provider.ConfigureRequest{Config: tfsdk.Config{Schema: sresp.Schema, Raw: raw}}, &resp)

	data, _ := resp.ResourceData.(*ProviderData)
	return data, resp.Diagnostics
}

const testHost = "https://example.church.tools"

// The credential rules. Two credentials at once is refused rather than resolved
// by precedence: an author who believes the one they just added is in use, and
// is wrong, authenticates as the wrong identity with nothing to tell them so.
func TestConfigure_CredentialModes(t *testing.T) {
	for _, tc := range []struct {
		name                string
		token, cookie, csrf string
		wantErr             bool
		wantErrContains     string
	}{
		{name: "token alone", token: "tok"},
		{name: "session pair", cookie: "sid=abc", csrf: "csrf-1"},
		{name: "nothing at all", wantErr: true, wantErrContains: "token"},
		{name: "both modes", token: "tok", cookie: "sid=abc", csrf: "csrf-1", wantErr: true},
		{name: "token plus a stray cookie", token: "tok", cookie: "sid=abc", wantErr: true},
		// The cookie alone authenticates reads, so this config plans fine and
		// fails on the first write. Refuse it while the message can still be clear.
		{name: "cookie without csrf", cookie: "sid=abc", wantErr: true},
		{name: "csrf without cookie", csrf: "csrf-1", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, diags := configureWith(t, testHost, tc.token, tc.cookie, tc.csrf)
			if got := diags.HasError(); got != tc.wantErr {
				t.Fatalf("HasError() = %v, want %v (%v)", got, tc.wantErr, diags)
			}
			if tc.wantErr {
				if tc.wantErrContains != "" && !strings.Contains(diags.Errors()[0].Detail(), tc.wantErrContains) {
					t.Errorf("detail %q does not mention %q", diags.Errors()[0].Detail(), tc.wantErrContains)
				}
				return
			}
			if data == nil || data.Client == nil {
				t.Fatal("Configure produced no client")
			}
		})
	}
}

// An unknown credential is the NORMAL case for the session pair: it arrives
// from a `data "external"` block running `ct auth token`, so its value does not
// exist at plan time. Configure must defer, not report a missing credential.
func TestConfigure_DefersOnUnknownCredential(t *testing.T) {
	p := New("test")()
	var sresp provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &sresp)

	attrTypes := map[string]tftypes.Type{
		"host":           tftypes.String,
		"token":          tftypes.String,
		"session_cookie": tftypes.String,
		"csrf_token":     tftypes.String,
	}
	raw := tftypes.NewValue(tftypes.Object{AttributeTypes: attrTypes}, map[string]tftypes.Value{
		"host":           tftypes.NewValue(tftypes.String, testHost),
		"token":          tftypes.NewValue(tftypes.String, ""),
		"session_cookie": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"csrf_token":     tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	var resp provider.ConfigureResponse
	p.Configure(context.Background(),
		provider.ConfigureRequest{Config: tfsdk.Config{Schema: sresp.Schema, Raw: raw}}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an unknown session must defer, not error: %v", resp.Diagnostics)
	}
	if resp.ResourceData != nil {
		t.Error("no client should be built from an unknown credential")
	}
}
