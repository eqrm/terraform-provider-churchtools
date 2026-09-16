// Package provider implements the ChurchTools OpenTofu/Terraform provider.
//
// Resource type names carry the provider prefix (`churchtools_campus`), which
// is what every Metadata below emits and what the golden fixtures assert.
package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
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
	// host/token may be unknown at plan time when they come from another
	// resource's output; the framework calls Configure anyway, so defer
	// instead of reporting a spurious config error.
	if cfg.Host.IsUnknown() || cfg.Token.IsUnknown() {
		return
	}
	if msg := validateHost(cfg.Host.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(path.Root("host"), "Ungueltiger ChurchTools-Host", msg)
	}
	if strings.TrimSpace(cfg.Token.ValueString()) == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Fehlender ChurchTools-Token",
			"token darf nicht leer sein.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	data := &ProviderData{Host: cfg.Host.ValueString(), Token: cfg.Token.ValueString()}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *churchtoolsProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewCampusResource,
	}
}

func (p *churchtoolsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

// validateHost rejects the two config mistakes that otherwise surface far away
// as `unsupported protocol scheme ""` from inside net/http: an empty host, and
// a bare hostname written without the scheme the description asks for.
func validateHost(host string) string {
	if strings.TrimSpace(host) == "" {
		return "host darf nicht leer sein, erwartet wird z. B. https://example.church.tools"
	}
	u, err := url.Parse(host)
	if err != nil {
		return fmt.Sprintf("host ist keine gueltige URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Sprintf("host braucht ein http(s)-Schema, z. B. https://example.church.tools (bekommen: %q)", host)
	}
	if u.Host == "" {
		return fmt.Sprintf("host enthaelt keinen Hostnamen (bekommen: %q)", host)
	}
	return ""
}
