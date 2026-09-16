package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
)

func TestProvider_Schema(t *testing.T) {
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	for _, name := range []string{"host", "token"} {
		if _, ok := resp.Schema.Attributes[name]; !ok {
			t.Errorf("missing provider attribute %q", name)
		}
	}
	if !resp.Schema.Attributes["token"].IsSensitive() {
		t.Error("token attribute must be marked sensitive")
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
