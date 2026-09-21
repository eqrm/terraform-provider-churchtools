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

const personStatusCollection = "/statuses"

type personStatusResource struct{ client *client.Client }

type personStatusModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Shorty          types.String `tfsdk:"shorty"`
	IsMember        types.Bool   `tfsdk:"is_member"`
	IsSearchable    types.Bool   `tfsdk:"is_searchable"`
	SortKey         types.Int64  `tfsdk:"sort_key"`
	SecurityLevelID types.Int64  `tfsdk:"security_level_id"`
}

func NewPersonStatusResource() resource.Resource { return &personStatusResource{} }

func (r *personStatusResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_person_status"
}

func (r *personStatusResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ein ChurchTools Personenstatus. Achtung: Das Löschen eines Status in " +
			"ChurchTools ändert jede Person, die ihn trägt — ChurchTools stempelt sie um. " +
			"Dieser Provider löscht nie.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name":              schema.StringAttribute{Required: true},
			"shorty":            schema.StringAttribute{Required: true},
			"is_member":         schema.BoolAttribute{Required: true},
			"is_searchable":     schema.BoolAttribute{Required: true},
			"sort_key":          schema.Int64Attribute{Required: true},
			"security_level_id": schema.Int64Attribute{Required: true},
		},
	}
}

func (r *personStatusResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *personStatusResource) managed(m personStatusModel) client.Row {
	return client.Row{
		"name":            m.Name.ValueString(),
		"shorty":          m.Shorty.ValueString(),
		"isMember":        m.IsMember.ValueBool(),
		"isSearchable":    m.IsSearchable.ValueBool(),
		"sortKey":         m.SortKey.ValueInt64(),
		"securityLevelId": m.SecurityLevelID.ValueInt64(),
	}
}

func (r *personStatusResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan personStatusModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Create(ctx, personStatusCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Personenstatus konnte nicht angelegt werden", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Status konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *personStatusResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state personStatusModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, personStatusCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Personenstatus konnte nicht gelesen werden", err.Error())
		return
	}
	state.Name = types.StringValue(stringField(row, "name"))
	state.Shorty = types.StringValue(stringField(row, "shorty"))
	state.IsMember = types.BoolValue(boolField(row, "isMember"))
	state.IsSearchable = types.BoolValue(boolField(row, "isSearchable"))
	state.SortKey = types.Int64Value(intField(row, "sortKey"))
	state.SecurityLevelID = types.Int64Value(intField(row, "securityLevelId"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *personStatusResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state personStatusModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, personStatusCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Personenstatus konnte nicht aktualisiert werden", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *personStatusResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Personenstatus"))
}

func (r *personStatusResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
