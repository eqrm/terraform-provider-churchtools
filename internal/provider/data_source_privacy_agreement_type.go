package provider

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// privacyAgreementTypesKey is the getMasterData row set behind the table
// cdb_privacy_policy_agreement_types ("how was the privacy policy agreed to").
// ChurchTools exposes it through NO REST endpoint -- only the per-person
// /persons/{id}/privacypolicy record -- so this is a data source over the legacy
// master-data READ, and deliberately not a resource: writing that table would
// mean widening the saveMasterData allowlist, which stays a separate decision.
const privacyAgreementTypesKey = "privacy_policy_agreement_types"

type privacyAgreementTypeDataSource struct{ client *client.Client }

type privacyAgreementTypeModel struct {
	Name      types.String `tfsdk:"name"`
	ID        types.String `tfsdk:"id"`
	SortKey   types.Int64  `tfsdk:"sort_key"`
	Deletable types.Bool   `tfsdk:"deletable"`
}

func NewPrivacyAgreementTypeDataSource() datasource.DataSource {
	return &privacyAgreementTypeDataSource{}
}

func (d *privacyAgreementTypeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_privacy_agreement_type"
}

func (d *privacyAgreementTypeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Liest eine Datenschutz-Zustimmungsart (wie wurde zugestimmt) anhand ihres Namens. " +
			"Nur lesend: ChurchTools bietet dafür keine REST-Schnittstelle.",
		Attributes: map[string]schema.Attribute{
			// The stored `bezeichnung`: a translation key for the built-ins
			// ("privacy.policy.agreement.type.checkin"), plain text for custom rows.
			"name":      schema.StringAttribute{Required: true},
			"id":        schema.StringAttribute{Computed: true},
			"sort_key":  schema.Int64Attribute{Computed: true},
			"deletable": schema.BoolAttribute{Computed: true},
		},
	}
}

// Configure unwraps the provider's shared *ProviderData exactly as
// configureClient does for resources; a nil ProviderData means the provider
// deferred on an unknown credential, and Read reports that via notConfigured.
func (d *privacyAgreementTypeDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*ProviderData)
	if !ok {
		resp.Diagnostics.AddError(notConfiguredSummary,
			fmt.Sprintf("Unerwarteter Provider-Datentyp: %T", req.ProviderData))
		return
	}
	d.client = data.Client
}

// legacyInt parses the legacy payload's stringly numbers, tolerating real ones.
func legacyInt(v any) (int64, bool) {
	switch n := v.(type) {
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	case float64:
		return int64(n), true
	}
	return 0, false
}

func (d *privacyAgreementTypeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if notConfigured(d.client, &resp.Diagnostics) {
		return
	}
	var cfg privacyAgreementTypeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rows, err := d.client.MasterDataRows(ctx, privacyAgreementTypesKey)
	if err != nil {
		resp.Diagnostics.AddError("Lesen fehlgeschlagen", err.Error())
		return
	}

	want := cfg.Name.ValueString()
	var matches []map[string]any
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		name := stringField(row, "bezeichnung")
		names = append(names, name)
		if name == want {
			matches = append(matches, row)
		}
	}
	sort.Strings(names)
	switch len(matches) {
	case 0:
		resp.Diagnostics.AddError("Datenschutz-Zustimmungsart nicht gefunden",
			fmt.Sprintf("Auf dieser Instanz gibt es keine Zustimmungsart %q. Vorhanden: %v. "+
				"Lege sie in ChurchTools unter Stammdaten an.", want, names))
		return
	case 1:
	default:
		// Names are not guaranteed unique in the legacy table; picking one
		// silently would bind a key to whichever row the map iterated first.
		resp.Diagnostics.AddError("Datenschutz-Zustimmungsart mehrdeutig",
			fmt.Sprintf("%d Zustimmungsarten heißen %q.", len(matches), want))
		return
	}

	row := matches[0]
	id, ok := legacyInt(row["id"])
	if !ok {
		resp.Diagnostics.AddError("Lesen fehlgeschlagen", fmt.Sprintf("Zustimmungsart %q hat keine lesbare id.", want))
		return
	}
	sortKey, _ := legacyInt(row["sortkey"])
	deletable, _ := legacyInt(row["deletable"])
	cfg.ID = types.StringValue(strconv.FormatInt(id, 10))
	cfg.SortKey = types.Int64Value(sortKey)
	cfg.Deletable = types.BoolValue(deletable == 1)
	resp.Diagnostics.Append(resp.State.Set(ctx, cfg)...)
}
