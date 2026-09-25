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

	"github.com/eqrm/terraform-provider-churchtools/internal/client"

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
//
// It carries ONE shared *client.Client on purpose. The framework builds a fresh
// resource per RPC and calls Configure each time, so a client constructed in
// configureClient would be thrown away after every call -- and with it the
// legacy session, making each department write pay whoami + csrftoken again.
type ProviderData struct {
	Host   string
	Client *client.Client
}

type providerModel struct {
	Host          types.String `tfsdk:"host"`
	Token         types.String `tfsdk:"token"`
	SessionCookie types.String `tfsdk:"session_cookie"`
	CSRFToken     types.String `tfsdk:"csrf_token"`
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
				Optional:  true,
				Sensitive: true,
				Description: "ChurchTools login token. Permanent and unscopable, and an " +
					"administrator credential on production — prefer session_cookie/csrf_token.",
			},
			"session_cookie": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "A ChurchTools session cookie, as emitted by `ct auth token`. " +
					"Set together with csrf_token, instead of token.",
			},
			"csrf_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				Description: "The CSRF token belonging to session_cookie, as emitted by " +
					"`ct auth token`. Set together with session_cookie, instead of token.",
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
	// Any credential may be unknown at plan time when it comes from another
	// resource's output -- which is the NORMAL case for the session pair, since
	// it typically arrives from a `data "external"` block running `ct auth
	// token`. The framework calls Configure anyway, so defer instead of
	// reporting a spurious config error.
	if cfg.Host.IsUnknown() || cfg.Token.IsUnknown() ||
		cfg.SessionCookie.IsUnknown() || cfg.CSRFToken.IsUnknown() {
		return
	}
	if msg := validateHost(cfg.Host.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(path.Root("host"), "Ungueltiger ChurchTools-Host", msg)
	}

	token := strings.TrimSpace(cfg.Token.ValueString())
	cookie := strings.TrimSpace(cfg.SessionCookie.ValueString())
	csrf := strings.TrimSpace(cfg.CSRFToken.ValueString())

	switch {
	case token == "" && cookie == "" && csrf == "":
		resp.Diagnostics.AddError("Fehlende ChurchTools-Anmeldedaten",
			"Es muss entweder token oder session_cookie + csrf_token gesetzt sein. "+
				"Bevorzugt wird die Session: `ct auth token` liefert sie, und der Login-Token "+
				"bleibt im Keychain.")
	case token != "" && (cookie != "" || csrf != ""):
		// Refused rather than silently preferring one. Two credentials in a config
		// usually means the author believes the one they just added is in use; if
		// that belief is wrong they are authenticating as the wrong identity, and
		// nothing downstream would say so.
		resp.Diagnostics.AddError("Widerspruechliche ChurchTools-Anmeldedaten",
			"token und session_cookie/csrf_token schliessen sich aus. Genau eine Variante setzen.")
	case cookie != "" && csrf == "":
		// The cookie alone authenticates GETs, so a plan would succeed and the
		// first write would fail. Refuse at configure time instead.
		resp.Diagnostics.AddAttributeError(path.Root("csrf_token"), "Unvollstaendige Session",
			"session_cookie ist gesetzt, csrf_token fehlt. Ohne CSRF-Token schlaegt jeder "+
				"Schreibvorgang fehl, waehrend plan noch funktioniert.")
	case csrf != "" && cookie == "":
		resp.Diagnostics.AddAttributeError(path.Root("session_cookie"), "Unvollstaendige Session",
			"csrf_token ist gesetzt, session_cookie fehlt.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	host := cfg.Host.ValueString()
	api := client.New(host, token)
	if token == "" {
		api = client.NewWithSession(host, cookie, csrf)
	}

	data := &ProviderData{Host: host, Client: api}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *churchtoolsProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewCampusResource,
		NewGroupTypeResource,
		NewDepartmentResource,
		NewPersonStatusResource,
		NewCommentViewerResource,
		NewContactLabelResource,
		NewRelationshipTypeResource,
	}
}

func (p *churchtoolsProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewPrivacyAgreementTypeDataSource,
	}
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
