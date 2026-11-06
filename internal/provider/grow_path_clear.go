package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Clearing a Wachstumspfad stage.
//
// grow_path_id is Optional+Computed so that "left out" means "unmanaged": the
// live value is kept and never compared. Terraform cannot tell an explicit null
// from an omitted attribute, so null can never mean "no stage". Clearing is a
// separate switch instead: clear_grow_path = true states "this role has no
// stage" and is enforced like a value would be (drift is corrected). ChurchTools
// types growPathId as integer|null on both PUT /group/roles/{id} and
// PATCH /groups/{id}/roles/{roleId} (OpenAPI 3.137); the null write itself has
// only been run against the mock.

// clearGrowPathAttribute is the schema of clear_grow_path.
func clearGrowPathAttribute() schema.BoolAttribute {
	return schema.BoolAttribute{
		Optional: true,
		Description: "true: this role has no growth path stage (sends growPathId: null if one is set, and corrects " +
			"a stage that someone sets later). Mutually exclusive with grow_path_id. false or left out: " +
			"grow_path_id decides. Terraform cannot tell null from a left-out attribute, which is why " +
			"clearing is its own switch.",
	}
}

// growPathPlan is the plan modifier of grow_path_id: null while clear_grow_path
// is true, otherwise the usual "keep the state value when left out".
func growPathPlan() planmodifier.Int64 { return growPathPlanModifier{} }

type growPathPlanModifier struct{}

func (growPathPlanModifier) Description(context.Context) string {
	return "Null when clear_grow_path is true, else the state value when grow_path_id is left out."
}

func (m growPathPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (growPathPlanModifier) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	var clear types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("clear_grow_path"), &clear)...)
	if req.ConfigValue.IsNull() {
		if clear.IsUnknown() {
			// Known only at apply: the stage may become null then.
			resp.PlanValue = types.Int64Unknown()
			return
		}
		if clear.ValueBool() {
			resp.PlanValue = types.Int64Null()
			return
		}
	}
	int64planmodifier.UseStateForUnknown().PlanModifyInt64(ctx, req, resp)
}

// validateGrowPathClear refuses a value together with the clear switch.
func validateGrowPathClear(growPath types.Int64, clear types.Bool, diags *diag.Diagnostics) {
	if clear.ValueBool() && !growPath.IsNull() && !growPath.IsUnknown() {
		diags.AddAttributeError(path.Root("clear_grow_path"), "grow_path_id und clear_grow_path widersprechen sich",
			"clear_grow_path = true heißt „keine Stufe“. Entweder grow_path_id setzen oder clear_grow_path, nicht beides.")
	}
}
