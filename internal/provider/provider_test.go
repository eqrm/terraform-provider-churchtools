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
