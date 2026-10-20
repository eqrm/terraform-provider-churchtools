package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Delete never reaches ChurchTools (no client is configured here) and warns
// like every other resource, naming the role it left behind.
func TestGroupRoleDefinition_DeleteWarnsOrphan(t *testing.T) {
	ctx := context.Background()
	r := NewGroupRoleDefinitionResource()
	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	sch := sresp.Schema
	state := tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	if d := state.Set(ctx, groupRoleDefinitionModel{
		ID: types.StringValue("295"), GroupTypeID: types.StringValue("57"), Name: types.StringValue("Mentor"),
		Type: types.StringNull(), SortKey: types.Int64Null(), Shorty: types.StringNull(),
		IsDefault: types.BoolNull(), IsHidden: types.BoolNull(),
		NameTranslated: types.StringNull(), GrowPathID: types.Int64Null(),
	}); d.HasError() {
		t.Fatalf("building state: %v", d)
	}

	var resp resource.DeleteResponse
	r.Delete(ctx, resource.DeleteRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete errored: %v", resp.Diagnostics)
	}
	w := resp.Diagnostics.Warnings()
	if len(w) != 1 || w[0].Summary() != orphanOnDeleteSummary {
		t.Fatalf("warnings = %v, want one %q", w, orphanOnDeleteSummary)
	}
	if d := w[0].Detail(); !strings.Contains(d, "295") || !strings.Contains(d, "Mentor") {
		t.Errorf("detail %q does not name the role", d)
	}
}
