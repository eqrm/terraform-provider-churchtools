package provider

import (
	"context"
	"testing"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/eqrm/terraform-provider-churchtools/internal/testmock"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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

// A config that leaves is_active out must PUT the value the instance holds NOW,
// not the one in state. UseStateForUnknown copies the state value into the plan,
// so the plan alone cannot tell "omitted" from "set": with -refresh=false, or a
// UI change landing between refresh and apply, a plan-sourced value would switch
// a field back on that someone just switched off. An acceptance step cannot
// reach this -- it always refreshes first -- so Update is called directly with
// state and plan that disagree with the instance.
func TestDBFieldUpdate_OmittedIsActiveTakesTheInstanceValue(t *testing.T) {
	ctx := context.Background()
	mock := testmock.New()
	defer mock.Close()
	mock.Seed("/dbfields", 19, map[string]any{
		"key":              "job",
		"isActive":         false, // switched off in the UI after the last refresh
		"isNewPersonField": false,
		"useAsPlaceholder": false,
		"securityLevel":    float64(3),
		"sortKey":          float64(4),
		"deleteOnArchive":  false,
	})

	r := &dbFieldResource{client: client.New(mock.URL, "token")}
	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	sch := sresp.Schema

	raw := func(m dbFieldModel) tftypes.Value {
		s := tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
		if d := s.Set(ctx, m); d.HasError() {
			t.Fatalf("building value: %v", d)
		}
		return s.Raw
	}
	stale := dbFieldModel{
		ID: types.StringValue("19"), Key: types.StringValue("job"),
		IsNewPersonField: types.BoolValue(false), IsActive: types.BoolValue(true),
	}
	plan := stale
	plan.IsNewPersonField = types.BoolValue(true)
	cfg := dbFieldModel{
		ID: types.StringNull(), Key: types.StringNull(),
		IsNewPersonField: types.BoolValue(true), IsActive: types.BoolNull(),
	}

	req := resource.UpdateRequest{
		Config: tfsdk.Config{Schema: sch, Raw: raw(cfg)},
		Plan:   tfsdk.Plan{Schema: sch, Raw: raw(plan)},
		State:  tfsdk.State{Schema: sch, Raw: raw(stale)},
	}
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: sch, Raw: raw(plan)}}
	r.Update(ctx, req, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Update: %v", resp.Diagnostics)
	}

	if got := mock.LastPut("/dbfields")["isActive"]; got != false {
		t.Errorf("PUT isActive = %v, want false (the instance's value; is_active is not in the config)", got)
	}
	var after dbFieldModel
	resp.State.Get(ctx, &after)
	if after.IsActive.ValueBool() {
		t.Errorf("state is_active = true, want false (what was written)")
	}
}
