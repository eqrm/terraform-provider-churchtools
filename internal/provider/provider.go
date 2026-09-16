// Package provider implements the ChurchTools OpenTofu/Terraform provider.
//
// Resource type names are BARE (`campus`, not `churchtools_campus`): a
// ct-structure config is 100% this provider, so the prefix would be noise on
// every line of a file admins are meant to read.
package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type churchtoolsProvider struct {
	version string
}

// ProviderData is handed to every resource's Configure.
type ProviderData struct {
	Host  string
	Token string
}

type providerModel struct {
	Host  types.String `tfsdk:"host"`
	Token types.String `tfsdk:"token"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &churchtoolsProvider{version: version} }
}

func (p *churchtoolsProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "churchtools"
	resp.Version = p.version
}

func (p *churchtoolsProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Required:    true,
				Description: "ChurchTools instance base URL, e.g. https://example.church.tools",
			},
			"token": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "ChurchTools login token.",
			},
		},
	}
}

func (p *churchtoolsProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	data := &ProviderData{Host: cfg.Host.ValueString(), Token: cfg.Token.ValueString()}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *churchtoolsProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{}
}

func (p *churchtoolsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}
