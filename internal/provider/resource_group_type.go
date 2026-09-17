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

const groupTypeCollection = "/group/grouptypes"

type groupTypeResource struct{ client *client.Client }

type groupTypeModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	NameTranslated types.String `tfsdk:"name_translated"`
}

func NewGroupTypeResource() resource.Resource { return &groupTypeResource{} }

func (r *groupTypeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group_type"
}

func (r *groupTypeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ein ChurchTools Gruppentyp.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name":            schema.StringAttribute{Required: true},
			"name_translated": schema.StringAttribute{Required: true},
		},
	}
}

func (r *groupTypeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp)
}

// groupTypeCreateDefaults mirrors ct-cli's registry `createDefaults`.
// ChurchTools rejects a create without them. They are NOT managed fields: the
// provider sends them once and never diffs them afterwards.
func groupTypeCreateDefaults(name string) client.Row {
	return client.Row{
		"namePlural":            truncatePadded(name, 30, 2),
		"shorty":                truncatePadded(name, 10, 1),
		"color":                 "default", // theme-neutral member of CT's palette
		"permissionDepth":       1,         // least privilege: the group's own members only
		"isLeaderNecessary":     false,
		"availableForNewPerson": false,
		"sortKey":               0,
		"postsEnabled":          false,
	}
}

// truncatePadded clips `name` to `max` runes and pads short names with trailing
// dots so ChurchTools' minimum-length validation passes.
func truncatePadded(name string, max, pad int) string {
	runes := []rune(name)
	if len(runes) > max {
		return string(runes[:max])
	}
	for len(runes) < pad {
		runes = append(runes, '.')
	}
	return string(runes)
}

func (r *groupTypeResource) managed(m groupTypeModel) client.Row {
	return client.Row{"name": m.Name.ValueString(), "nameTranslated": m.NameTranslated.ValueString()}
}

func (r *groupTypeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan groupTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := groupTypeCreateDefaults(plan.Name.ValueString())
	for k, v := range r.managed(plan) {
		body[k] = v
	}
	row, err := r.client.Create(ctx, groupTypeCollection, body)
	if err != nil {
		resp.Diagnostics.AddError("Gruppentyp konnte nicht angelegt werden", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Gruppentyp konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupTypeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state groupTypeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, groupTypeCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Gruppentyp konnte nicht gelesen werden", err.Error())
		return
	}
	state.Name = types.StringValue(stringField(row, "name"))
	state.NameTranslated = types.StringValue(stringField(row, "nameTranslated"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *groupTypeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state groupTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, groupTypeCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Gruppentyp konnte nicht aktualisiert werden", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *groupTypeResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Gruppentypen"))
}

func (r *groupTypeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
