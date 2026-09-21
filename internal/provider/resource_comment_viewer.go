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

const commentViewerCollection = "/person/commentviewers"

type commentViewerResource struct{ client *client.Client }

type commentViewerModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	SortKey types.Int64  `tfsdk:"sort_key"`
}

func NewCommentViewerResource() resource.Resource { return &commentViewerResource{} }

func (r *commentViewerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_comment_viewer"
}

func (r *commentViewerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine ChurchTools Kommentar-Sichtbarkeit.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true},
			// Optional+Computed with NO static default: a config that omits
			// sort_key keeps whatever ChurchTools already has. A StaticInt64(0)
			// default would instead rewrite an imported row's real sortKey (say
			// 30) down to 0 on the next apply, reordering the list instance-wide.
			// New rows still get 0, which is ct-cli's `sortKey ?? 0`.
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *commentViewerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *commentViewerResource) managed(m commentViewerModel) client.Row {
	return client.Row{
		"name":    m.Name.ValueString(),
		"sortKey": m.SortKey.ValueInt64(),
	}
}

func (r *commentViewerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan commentViewerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// See resource_department.go: omitted sort_key arrives unknown, and a NEW
	// row gets ct-cli's `sortKey ?? 0`.
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	row, err := r.client.Create(ctx, commentViewerCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Anlegen fehlgeschlagen", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Kommentar-Sichtbarkeit konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *commentViewerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state commentViewerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, commentViewerCollection, state.ID.ValueString())
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

func (r *commentViewerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state commentViewerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, commentViewerCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Aktualisieren fehlgeschlagen", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *commentViewerResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Kommentar-Sichtbarkeiten"))
}

func (r *commentViewerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
