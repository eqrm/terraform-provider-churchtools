package provider

import (
	"context"
	"errors"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A ChurchTools data field (Datenfeld, /dbfields) — the definition of a column
// on the person or group record, not any person's value in it.
//
// IMPORT-ONLY, and it manages exactly TWO settings: isNewPersonField ("Beim
// Anlegen einer Person abfragen") and isActive ("Aktiv"). The fields this exists
// for are ChurchTools built-ins (statusId, id 32, is the reason for the first:
// ct-structure hides it from the create-person dialog, IT-8; birthplace, job and
// nationalityId for the second: IT-29 switches them off), so Create refuses
// rather than POSTing a custom field, and Delete orphans like every other
// resource here.
//
// is_active is Optional+Computed: a config that leaves it out keeps whatever the
// instance has, so the statusId import from before this attribute existed plans
// no change. A built-in CAN be inactive — eqrm-dev ships isSystemUser that way.
//
// The write contract was measured on eqrm-dev (CT 3.137.0-RC22) on 2026-09-29, not
// read off the spec alone:
//
//   - PATCH /dbfields/{id} → "Method not allowed. Must be one of: GET, PUT, DELETE".
//   - A partial PUT {isNewPersonField} → 400 validation errors on
//     useAsPlaceholder, securityLevel, sortKey and deleteOnArchive: PUT replaces.
//   - PUT with exactly the spec's request keys (dbFieldPutKeys), taken from a
//     fresh GET, → 200; a GET afterwards differs from the one before only in
//     what was changed (no-op on fields 32, 12, 14; a real toggle of 32 and its
//     restore).
//
// Echoing the GET verbatim (nested fieldCategory/fieldType, @deprecated, …) was
// also accepted, but that relies on CT silently ignoring read-only keys; the
// spec-shaped body is the documented contract, so that is what Update sends.
const dbFieldCollection = "/dbfields"

// dbFieldPutKeys is the PUT /dbfields/{id} request body per the CT OpenAPI spec:
// required deleteOnArchive, isActive, isNewPersonField, lineEnding, name,
// securityLevel, sortKey, useAsPlaceholder; optional length, shorty; plus id.
// Every value except isNewPersonField and isActive is copied from a GET made
// immediately before the PUT, so the write changes nothing this resource does
// not manage.
var dbFieldPutKeys = []string{
	"id",
	"name",
	"shorty",
	"length",
	"lineEnding",
	"securityLevel",
	"sortKey",
	"isActive",
	"useAsPlaceholder",
	"isNewPersonField",
	"deleteOnArchive",
}

type dbFieldResource struct{ client *client.Client }

type dbFieldModel struct {
	ID               types.String `tfsdk:"id"`
	Key              types.String `tfsdk:"key"`
	IsNewPersonField types.Bool   `tfsdk:"is_new_person_field"`
	IsActive         types.Bool   `tfsdk:"is_active"`
}

func NewDBFieldResource() resource.Resource { return &dbFieldResource{} }

func (r *dbFieldResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_db_field"
}

func (r *dbFieldResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Eine Einstellung eines vorhandenen ChurchTools-Datenfelds. Nur importierbar: " +
			"der Provider legt keine Datenfelder an und löscht keine, er verwaltet nur " +
			"„Beim Anlegen einer Person abfragen“ (is_new_person_field) und „Aktiv“ (is_active).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// Read-only: shows WHICH field an id is (e.g. "statusId"), so a
			// config that imports the wrong id surfaces in the plan.
			"key": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"is_new_person_field": schema.BoolAttribute{Required: true},
			// Optional: left out, the instance's value is kept (see the type comment).
			"is_active": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *dbFieldResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = configureClient(req, resp, r.client)
}

// The not-configured check comes FIRST, before the import-only refusal: a
// provider with no host is a different problem from asking for an unsupported
// create, and naming the wrong one sends the reader to the wrong file.
func (r *dbFieldResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.AddError(
		"Datenfeld kann nicht angelegt werden",
		"Dieser Provider legt keine Datenfelder an — er verwaltet nur Einstellungen vorhandener "+
			"Felder (z. B. des Built-ins statusId, id 32). Importiere das Feld stattdessen, z. B.:\n\n"+
			"  import {\n    to = churchtools_db_field.<name>\n    id = \"32\"\n  }")
}

func (r *dbFieldResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var state dbFieldModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	row, err := r.client.Get(ctx, dbFieldCollection, state.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Datenfeld konnte nicht gelesen werden", err.Error())
		return
	}
	state.Key = types.StringValue(stringField(row, "key"))
	state.IsNewPersonField = types.BoolValue(boolField(row, "isNewPersonField"))
	state.IsActive = types.BoolValue(boolField(row, "isActive"))
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// dbFieldPutBody builds the full PUT body from a fresh GET: CT has no PATCH here and a
// partial PUT is a 400, so every required key is carried over unchanged and only
// isNewPersonField and isActive take the planned values. A key the GET did not
// return is left out rather than invented — CT's own validation error then names it.
func dbFieldPutBody(current client.Row, isNewPersonField, isActive bool) client.Row {
	body := client.Row{}
	for _, k := range dbFieldPutKeys {
		if v, ok := current[k]; ok {
			body[k] = v
		}
	}
	body["isNewPersonField"] = isNewPersonField
	body["isActive"] = isActive
	return body
}

func (r *dbFieldResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if notConfigured(r.client, &resp.Diagnostics) {
		return
	}
	var plan, state dbFieldModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	current, err := r.client.Get(ctx, dbFieldCollection, id)
	if err != nil {
		resp.Diagnostics.AddError("Datenfeld konnte nicht gelesen werden", err.Error())
		return
	}
	// Ask the CONFIG whether is_active is managed, not the plan: UseStateForUnknown
	// copies the state value into the plan, so an omitted is_active looks set there,
	// and with -refresh=false (or a UI change after refresh) that stale value would
	// be written back. Omitted means: keep what the instance holds now.
	var configured types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("is_active"), &configured)...)
	if resp.Diagnostics.HasError() {
		return
	}
	isActive := boolField(current, "isActive")
	if !configured.IsNull() && !configured.IsUnknown() {
		isActive = configured.ValueBool()
	}
	if _, err := r.client.Update(ctx, dbFieldCollection, id, "PUT",
		dbFieldPutBody(current, plan.IsNewPersonField.ValueBool(), isActive)); err != nil {
		resp.Diagnostics.AddError("Datenfeld konnte nicht aktualisiert werden", err.Error())
		return
	}
	plan.ID = state.ID
	plan.Key = types.StringValue(stringField(current, "key"))
	plan.IsActive = types.BoolValue(isActive)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *dbFieldResource) Delete(_ context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.AddWarning(orphanOnDeleteSummary, orphanOnDeleteDetail("Datenfelder"))
}

func (r *dbFieldResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
