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

// A ChurchTools Gruppen-Kategorie. REST on /group/groupcategories.
//
// `color` is REQUIRED on create and validated server-side against a fixed
// enum — a POST without it is a 400 listing the accepted values (verified live
// on eqrm-dev). It is therefore Required here: a category without a colour is
// not a thing ChurchTools will accept, so defaulting one would only move the
// failure. The enum is NOT mirrored into a validator here on purpose; it is the
// instance's list and ChurchTools' error names every allowed value, which
// stays correct when ChurchTools adds one.
//
// `description` is nullable on every eqrm row, so it is Optional and only sent
// when set.
const groupCategoryCollection = "/group/groupcategories"

type groupCategoryResource struct{ client *client.Client }

type groupCategoryModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	SortKey     types.Int64  `tfsdk:"sort_key"`
	Color       types.String `tfsdk:"color"`
	Description types.String `tfsdk:"description"`
}

func NewGroupCategoryResource() resource.Resource { return &groupCategoryResource{} }

func (r *groupCategoryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_category"
}

func (r *groupCategoryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine ChurchTools Gruppen-Kategorie.",
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
			"color": schema.StringAttribute{
				Required:    true,
				Description: "Farbschlüssel, z. B. orange, violet, emerald, pink, cyan. Pflichtfeld der API.",
			},
			"description": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *groupCategoryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *groupCategoryResource) managed(m groupCategoryModel) client.Row {
	row := client.Row{
		"name":    m.Name.ValueString(),
		"sortKey": m.SortKey.ValueInt64(),
		"color":   m.Color.ValueString(),
	}
	// Null and unknown both mean "no opinion"; sending "" would blank a
	// description the instance already has.
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		row["description"] = m.Description.ValueString()
	}
	return row
}

func (r *groupCategoryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan groupCategoryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	row, err := r.client.Create(ctx, groupCategoryCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Anlegen fehlgeschlagen", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Gruppen-Kategorie konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	plan.Description = types.StringValue(stringField(row, "description"))
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupCategoryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state groupCategoryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, groupCategoryCollection, state.ID.ValueString())
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
	state.Color = types.StringValue(stringField(row, "color"))
	state.Description = types.StringValue(stringField(row, "description"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *groupCategoryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state groupCategoryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, groupCategoryCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Aktualisieren fehlgeschlagen", err.Error())
		return
	}
	// The PUT response is deliberately NOT read back. ChurchTools answers 204
	// with no body on /group/targetgroups and /group/agegroups and 200 with one
	// here, so a resource that parsed the response would work on one collection
	// and silently blank a field on the next. description is Optional+Computed
	// with UseStateForUnknown, so an omitted one is already carried over from
	// state and never arrives unknown here.
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupCategoryResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Gruppen-Kategorien"))
}

func (r *groupCategoryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
