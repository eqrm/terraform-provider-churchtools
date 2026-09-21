package provider

import (
	"context"
	"errors"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const campusCollection = "/campuses"

type campusResource struct {
	client *client.Client
}

type campusModel struct {
	ID     types.String `tfsdk:"id"`
	Name   types.String `tfsdk:"name"`
	Shorty types.String `tfsdk:"shorty"`
}

func NewCampusResource() resource.Resource { return &campusResource{} }

func (r *campusResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_campus"
}

func (r *campusResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ein ChurchTools Standort (campus).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "ChurchTools campus id. Can be \"0\".",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true, Description: "Display name."},
			"shorty": schema.StringAttribute{
				Required:    true,
				Description: "Short name (1-10 chars), required by ChurchTools on create.",
			},
		},
	}
}

func (r *campusResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *campusResource) body(m campusModel) client.Row {
	return client.Row{"name": m.Name.ValueString(), "shorty": m.Shorty.ValueString()}
}

func (r *campusResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan campusModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	row, err := r.client.Create(ctx, campusCollection, r.body(plan))
	if err != nil {
		resp.Diagnostics.AddError("Standort konnte nicht angelegt werden", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Standort konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *campusResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state campusModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	row, err := r.client.Get(ctx, campusCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Standort konnte nicht gelesen werden", err.Error())
		return
	}

	state.Name = types.StringValue(stringField(row, "name"))
	state.Shorty = types.StringValue(stringField(row, "shorty"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *campusResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state campusModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = state.ID
	if _, err := r.client.Update(ctx, campusCollection, state.ID.ValueString(), "PUT", r.body(plan)); err != nil {
		resp.Diagnostics.AddError("Standort konnte nicht aktualisiert werden", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete un-manages without deleting — see orphanOnDeleteDetail.
func (r *campusResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Standorte"))
}

func (r *campusResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
