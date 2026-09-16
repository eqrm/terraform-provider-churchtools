package provider

import (
	"context"
	"errors"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const LDepartmentCollection = "/departments"

type LDepartmentResource struct{ client *client.Client }

type LDepartmentModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Shorty  types.String `tfsdk:"shorty"`
	SortKey types.Int64  `tfsdk:"sort_key"`
}

func NewDepartmentResource() resource.Resource { return &LDepartmentResource{} }

func (r *LDepartmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_department"
}

func (r *LDepartmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ein ChurchTools Bereich. ChurchTools bietet keine REST-Löschung für Bereiche an; dieser Provider löscht ohnehin nie.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name":   schema.StringAttribute{Required: true},
			"shorty": schema.StringAttribute{Required: true},
			// ct-cli stores this as `sortKey ?? 0`; matching that keeps an
			// exported resource that omits it a no-op rather than a diff.
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(0),
			},
		},
	}
}

func (r *LDepartmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

func (r *LDepartmentResource) managed(m LDepartmentModel) client.Row {
	return client.Row{
		"name":    m.Name.ValueString(),
		"shorty":  m.Shorty.ValueString(),
		"sortKey": m.SortKey.ValueInt64(),
	}
}

func (r *LDepartmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LDepartmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Create(ctx, LDepartmentCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Anlegen fehlgeschlagen", err.Error())
		return
	}
	plan.ID = types.StringValue(idString(row["id"]))
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *LDepartmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LDepartmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, LDepartmentCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Lesen fehlgeschlagen", err.Error())
		return
	}
	state.Name = types.StringValue(stringField(row, "name"))
	state.Shorty = types.StringValue(stringField(row, "shorty"))
	state.SortKey = types.Int64Value(intField(row, "sortKey"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *LDepartmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state LDepartmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, LDepartmentCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Aktualisieren fehlgeschlagen", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *LDepartmentResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Bereiche"))
}

func (r *LDepartmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
