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

// relationshipTypeCollection is the catalog of person-to-person relationship
// TYPES (Eltern/Kind, Ehepaar, Abholberechtigt, …) -- never a relationship
// between two people, which this provider does not touch.
const relationshipTypeCollection = "/person/relationshiptypes"

type relationshipTypeResource struct{ client *client.Client }

type relationshipTypeModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	DegreeNameA     types.String `tfsdk:"degree_name_a"`
	DegreeNameB     types.String `tfsdk:"degree_name_b"`
	SecurityLevelID types.Int64  `tfsdk:"security_level_id"`
	SortKey         types.Int64  `tfsdk:"sort_key"`
	ExportTitle     types.String `tfsdk:"export_title"`
	IncludeInExport types.Bool   `tfsdk:"include_in_export"`
}

func NewRelationshipTypeResource() resource.Resource { return &relationshipTypeResource{} }

func (r *relationshipTypeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_relationship_type"
}

func (r *relationshipTypeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ein ChurchTools Beziehungstyp (Katalogeintrag, keine Beziehung zwischen Personen).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// Optional stored name or translation key. CT stores null for an
			// omitted name and derives the display name from the side labels, so
			// Optional+Computed: an imported built-in keeps its key untouched.
			"name": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// The two side labels. Identical labels make the type undirected.
			"degree_name_a": schema.StringAttribute{Required: true},
			"degree_name_b": schema.StringAttribute{Required: true},
			// An existing security level; CT answers 404 for an unknown id.
			"security_level_id": schema.Int64Attribute{Required: true},
			// The next three are Optional+Computed with no static default: the
			// same built-in type differs between instances here (parent-child is
			// sortKey 1 on one, 10 on another; exportTitle null vs ""), and a
			// static default would make one host-portable config fight both.
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"export_title": schema.StringAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"include_in_export": schema.BoolAttribute{
				Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *relationshipTypeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

// nullableString sends a known value as-is and null as JSON null. PUT treats an
// OMITTED name/exportTitle as null too, so every update must resend the current
// value -- which the plan carries via UseStateForUnknown.
func nullableString(v types.String) any {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return v.ValueString()
}

func (r *relationshipTypeResource) managed(m relationshipTypeModel) client.Row {
	return client.Row{
		"name":            nullableString(m.Name),
		"degreeNameA":     m.DegreeNameA.ValueString(),
		"degreeNameB":     m.DegreeNameB.ValueString(),
		"securityLevelId": m.SecurityLevelID.ValueInt64(),
		"sortKey":         m.SortKey.ValueInt64(),
		"exportTitle":     nullableString(m.ExportTitle),
		"includeInExport": m.IncludeInExport.ValueBool(),
	}
}

// fromRow copies CT's answer into the model. name and exportTitle stay null when
// CT holds null, so a state read back from CT equals the one written.
func (r *relationshipTypeResource) fromRow(row client.Row, m *relationshipTypeModel) {
	if s, ok := row["name"].(string); ok {
		m.Name = types.StringValue(s)
	} else {
		m.Name = types.StringNull()
	}
	m.DegreeNameA = types.StringValue(stringField(row, "degreeNameA"))
	m.DegreeNameB = types.StringValue(stringField(row, "degreeNameB"))
	m.SecurityLevelID = types.Int64Value(intField(row, "securityLevelId"))
	m.SortKey = types.Int64Value(intField(row, "sortKey"))
	if s, ok := row["exportTitle"].(string); ok {
		m.ExportTitle = types.StringValue(s)
	} else {
		m.ExportTitle = types.StringNull()
	}
	m.IncludeInExport = types.BoolValue(boolField(row, "includeInExport"))
}

func (r *relationshipTypeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan relationshipTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A NEW type gets CT's own defaults for whatever the config omits.
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	if plan.IncludeInExport.IsUnknown() || plan.IncludeInExport.IsNull() {
		plan.IncludeInExport = types.BoolValue(false)
	}
	if plan.Name.IsUnknown() {
		plan.Name = types.StringNull()
	}
	if plan.ExportTitle.IsUnknown() {
		plan.ExportTitle = types.StringNull()
	}
	row, err := r.client.Create(ctx, relationshipTypeCollection, r.managed(plan))
	if err != nil {
		resp.Diagnostics.AddError("Anlegen fehlgeschlagen", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Beziehungstyp konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *relationshipTypeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state relationshipTypeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, relationshipTypeCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Lesen fehlgeschlagen", err.Error())
		return
	}
	r.fromRow(row, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *relationshipTypeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state relationshipTypeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if _, err := r.client.Update(ctx, relationshipTypeCollection, state.ID.ValueString(), "PUT", r.managed(plan)); err != nil {
		resp.Diagnostics.AddError("Aktualisieren fehlgeschlagen", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *relationshipTypeResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Beziehungstypen"))
}

func (r *relationshipTypeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
