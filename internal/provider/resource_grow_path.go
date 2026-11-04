package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// churchtools_grow_path: a stage of the Wachstumspfad (growth path),
// `/person/growpaths`. CT computes a person's growPathId from their group
// roles: every group-type role may point at a stage, and the stage with the
// higher sortKey wins. PATCH /persons ignores growPathId, so the stages and the
// role -> stage mapping (churchtools_group_role_definition.grow_path_id) are the
// only levers.
//
// Contract (measured on eqrm-dev, checked against the OpenAPI spec): POST needs
// name and color and answers 201 with the row; PUT needs name and color and an
// OMITTED sortKey resets it to 0, so sortKey is always sent; an unknown id is
// 404 on GET and PUT. DELETE answers 409 while roles reference the stage, but
// like every resource here Delete only un-manages and never calls it.
const growPathCollection = "/person/growpaths"

var growPathColors = []string{
	"accent", "amber", "basic", "blue", "constructive", "critical", "cyan", "destructive",
	"emerald", "error", "fuchsia", "gray", "green", "indigo", "info", "lime", "magic",
	"orange", "pink", "purple", "red", "rose", "sky", "success", "teal", "violet",
	"warning", "yellow",
}

type growPathResource struct{ client *client.Client }

type growPathModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Color   types.String `tfsdk:"color"`
	SortKey types.Int64  `tfsdk:"sort_key"`
}

func NewGrowPathResource() resource.Resource { return &growPathResource{} }

var (
	_ resource.ResourceWithValidateConfig = &growPathResource{}
	_ resource.ResourceWithImportState    = &growPathResource{}
)

func (r *growPathResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_grow_path"
}

func (r *growPathResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine Stufe des Wachstumspfads. ChurchTools berechnet den Wachstumspfad einer Person aus ihren " +
			"Gruppenrollen; die Stufe mit dem höheren sort_key gewinnt. Die Zuordnung Rolle -> Stufe setzt " +
			"churchtools_group_role_definition.grow_path_id. Eine Änderung hier berechnet die bereits an " +
			"Personen gespeicherten Wachstumspfade nicht neu. Destroy entfernt die Stufe nur aus dem " +
			"Terraform-State und löscht sie nicht in ChurchTools (Warnung).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name der Stufe (1 bis 100 Zeichen).",
			},
			"color": schema.StringAttribute{
				Required:    true,
				Description: "Farbe der Stufe, einer von: " + strings.Join(growPathColors, ", ") + ".",
			},
			"sort_key": schema.Int64Attribute{
				Optional: true, Computed: true,
				Description: "Priorität (ganze Zahl ab 0): die höhere Stufe gewinnt. Weggelassen: 0 beim Anlegen, " +
					"danach unverwaltet. ChurchTools setzt einen weggelassenen sortKey beim Schreiben auf 0 zurück; " +
					"der Provider sendet ihn deshalb immer.",
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *growPathResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

func (r *growPathResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m growPathModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Name.IsUnknown() && !m.Name.IsNull() {
		if n := len([]rune(m.Name.ValueString())); n < 1 || n > 100 {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Ungültiger Name",
				"Der Name einer Wachstumspfad-Stufe hat 1 bis 100 Zeichen.")
		}
	}
	if !m.Color.IsUnknown() && !m.Color.IsNull() {
		if !slices.Contains(growPathColors, m.Color.ValueString()) {
			resp.Diagnostics.AddAttributeError(path.Root("color"), "Ungültige Farbe",
				fmt.Sprintf("%q: erlaubt sind %s.", m.Color.ValueString(), strings.Join(growPathColors, ", ")))
		}
	}
	if !m.SortKey.IsUnknown() && !m.SortKey.IsNull() && m.SortKey.ValueInt64() < 0 {
		resp.Diagnostics.AddAttributeError(path.Root("sort_key"), "Ungültiger sort_key",
			"sort_key ist eine ganze Zahl ab 0.")
	}
}

func (m growPathModel) body() client.Row {
	return client.Row{
		"name":    m.Name.ValueString(),
		"color":   m.Color.ValueString(),
		"sortKey": m.SortKey.ValueInt64(), // always sent: an omitted sortKey resets to 0
	}
}

func (m *growPathModel) fromRow(row client.Row) {
	m.Name = types.StringValue(stringField(row, "name"))
	m.Color = types.StringValue(stringField(row, "color"))
	m.SortKey = types.Int64Value(intField(row, "sortKey"))
}

func (r *growPathResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan growPathModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = types.Int64Value(0)
	}
	row, err := r.client.Create(ctx, growPathCollection, plan.body())
	if err != nil {
		resp.Diagnostics.AddError("Wachstumspfad-Stufe konnte nicht angelegt werden", err.Error())
		return
	}
	id, err := requireID(row)
	if err != nil {
		resp.Diagnostics.AddError("Wachstumspfad-Stufe konnte nicht angelegt werden", err.Error())
		return
	}
	plan.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *growPathResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state growPathModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, growPathCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) || isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Lesen fehlgeschlagen", err.Error())
		return
	}
	state.fromRow(row)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *growPathResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state growPathModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = state.ID
	if plan.SortKey.IsUnknown() || plan.SortKey.IsNull() {
		plan.SortKey = state.SortKey
	}
	if _, err := r.client.Update(ctx, growPathCollection, state.ID.ValueString(), "PUT", plan.body()); err != nil {
		resp.Diagnostics.AddError("Wachstumspfad-Stufe konnte nicht geändert werden", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete only drops the stage from Terraform state, like every resource here.
// ChurchTools would answer DELETE with 409 while group-type roles or group
// roles reference the stage, and a stage in use must not vanish by accident.
func (r *growPathResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Wachstumspfad-Stufen")+
		" ChurchTools verweigert das Löschen (409), solange Gruppentyp-Rollen oder Gruppenrollen die Stufe noch verwenden.")
}

func (r *growPathResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !digitsOnly.MatchString(req.ID) {
		resp.Diagnostics.AddError("Ungültige Import-id",
			fmt.Sprintf("%q: erwartet die id der Stufe (Zahl).", req.ID))
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
