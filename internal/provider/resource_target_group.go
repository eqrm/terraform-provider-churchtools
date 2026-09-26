package provider

import (
	"context"
	"errors"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A ChurchTools Zielgruppe. Plain REST, unlike the privacy-agreement types:
// GET/POST/PUT/DELETE on /group/targetgroups, verified live on eqrm-dev
// (POST returns 201 with data.id).
//
// `nameTranslated` is NOT managed. ChurchTools derives it from `name` for a
// custom row and from the translation catalogue for a built-in, so sending it
// would fight the instance on every apply.
const targetGroupCollection = "/group/targetgroups"

type targetGroupResource struct{ client *client.Client }

type targetGroupModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	SortKey types.Int64  `tfsdk:"sort_key"`
}

func NewTargetGroupResource() resource.Resource { return &targetGroupResource{} }

func (r *targetGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_target_group"
}

func (r *targetGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine ChurchTools Zielgruppe.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true},
			// Optional+Computed with no static default, as everywhere here: an
			// omitted sort_key keeps whatever ChurchTools has, so importing a
			// row does not renumber the instance's list on the next apply.
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *targetGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *targetGroupResource) managed(m targetGroupModel) client.Row {
	return client.Row{
		"name":    m.Name.ValueString(),
		"sortKey": m.SortKey.ValueInt64(),
	}
}

func (r *targetGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan targetGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	row, err := r.client.Create(ctx, targetGroupCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Anlegen fehlgeschlagen", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Zielgruppe konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *targetGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state targetGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, targetGroupCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Lesen fehlgeschlagen", err.Error())
		return
	}
	state.Name = types.StringValue(stringField(row, "name"))
	state.SortKey = types.Int64Value(intField(row, "sortKey"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *targetGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state targetGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, targetGroupCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Aktualisieren fehlgeschlagen", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *targetGroupResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Zielgruppen"))
}

func (r *targetGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
