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

// A ChurchTools Altersgruppe. REST on /group/agegroups, verified live on
// eqrm-dev (POST returns 201 with data.id).
//
// `start` and `end` are REQUIRED by the API on create — a POST without them is
// a 400 — so unlike sort_key they are Required here rather than
// Optional+Computed. On eqrm production every row is 0..99, i.e. the age window
// is not actually used to segment anything; it is still sent faithfully rather
// than defaulted, because the field is the instance's, not this provider's.
const ageGroupCollection = "/group/agegroups"

type ageGroupResource struct{ client *client.Client }

type ageGroupModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	SortKey types.Int64  `tfsdk:"sort_key"`
	Start   types.Int64  `tfsdk:"start"`
	End     types.Int64  `tfsdk:"end"`
}

func NewAgeGroupResource() resource.Resource { return &ageGroupResource{} }

func (r *ageGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_age_group"
}

func (r *ageGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine ChurchTools Altersgruppe.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true},
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"start": schema.Int64Attribute{
				Required:    true,
				Description: "Untere Altersgrenze. Pflichtfeld der API.",
			},
			"end": schema.Int64Attribute{
				Required:    true,
				Description: "Obere Altersgrenze. Pflichtfeld der API.",
			},
		},
	}
}

func (r *ageGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *ageGroupResource) managed(m ageGroupModel) client.Row {
	return client.Row{
		"name":    m.Name.ValueString(),
		"sortKey": m.SortKey.ValueInt64(),
		"start":   m.Start.ValueInt64(),
		"end":     m.End.ValueInt64(),
	}
}

func (r *ageGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan ageGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	row, err := r.client.Create(ctx, ageGroupCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Anlegen fehlgeschlagen", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Altersgruppe konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *ageGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state ageGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, ageGroupCollection, state.ID.ValueString())
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
	state.Start = types.Int64Value(intField(row, "start"))
	state.End = types.Int64Value(intField(row, "end"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *ageGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state ageGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, ageGroupCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Aktualisieren fehlgeschlagen", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *ageGroupResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Altersgruppen"))
}

func (r *ageGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
