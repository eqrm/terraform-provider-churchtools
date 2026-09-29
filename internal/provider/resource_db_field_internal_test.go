package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Delete never reaches ChurchTools; it only warns that the field lives on.
func TestDBField_DeleteWarnsOrphan(t *testing.T) {
	var resp resource.DeleteResponse
	NewDBFieldResource().Delete(context.Background(), resource.DeleteRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete errored: %v", resp.Diagnostics)
	}
	w := resp.Diagnostics.Warnings()
	if len(w) != 1 || w[0].Summary() != orphanOnDeleteSummary {
		t.Fatalf("warnings = %v, want one %q", w, orphanOnDeleteSummary)
	}
}
