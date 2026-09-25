package provider

import (
	"context"
	"errors"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// contactLabelCollection is ChurchTools' e-mail contact-label catalog ("Privat",
// "Arbeit", …). Full REST CRUD; PUT requires every field, so managed() always
// sends all three.
const contactLabelCollection = "/contactlabels"

type contactLabelResource struct{ client *client.Client }

type contactLabelModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	SortKey   types.Int64  `tfsdk:"sort_key"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

func NewContactLabelResource() resource.Resource { return &contactLabelResource{} }

func (r *contactLabelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_contact_label"
}

func (r *contactLabelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ein ChurchTools Kontakt-Label für E-Mail-Adressen.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// Built-in labels carry a translation key ("contact.label.private"),
			// custom ones plain text. Names are unique on the instance.
			"name": schema.StringAttribute{Required: true},
			// Optional+Computed with no static default, for the reason given on
			// comment_viewer.sort_key: omitting it keeps whatever CT has.
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			// Setting true makes CT UNSET the current default label instance-wide.
			// Optional+Computed so an imported label keeps its flag unless the
			// config says otherwise.
			"is_default": schema.BoolAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *contactLabelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *contactLabelResource) managed(m contactLabelModel) client.Row {
	return client.Row{
		"name":      m.Name.ValueString(),
		"sortKey":   m.SortKey.ValueInt64(),
		"isDefault": m.IsDefault.ValueBool(),
	}
}

func (r *contactLabelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan contactLabelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A NEW label gets sortKey 0 and is not the default unless the config says so.
	// CT requires both fields on create.
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	if plan.IsDefault.IsUnknown() || plan.IsDefault.IsNull() {
		plan.IsDefault = types.BoolValue(false)
	}
	row, err := r.client.Create(ctx, contactLabelCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Anlegen fehlgeschlagen", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Kontakt-Label konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *contactLabelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state contactLabelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, contactLabelCollection, state.ID.ValueString())
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
	state.IsDefault = types.BoolValue(boolField(row, "isDefault"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *contactLabelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state contactLabelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, contactLabelCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Aktualisieren fehlgeschlagen", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *contactLabelResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Kontakt-Labels"))
}

func (r *contactLabelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
