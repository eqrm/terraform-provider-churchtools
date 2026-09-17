package provider

import (
	"fmt"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// configureClient is the one Configure body every resource shares. Keeping it
// in one place means a change to how the provider is wired cannot be applied
// to four resources and forgotten on the fifth.
//
// It returns the provider's SHARED client, so the legacy session survives
// across the per-RPC resource instances the framework creates.
//
// `keep` is the resource's current client: when the framework calls Configure
// with no provider data yet, returning nil would otherwise wipe an already
// configured client and panic the plugin on the next CRUD call.
func configureClient(req resource.ConfigureRequest, resp *resource.ConfigureResponse, keep *client.Client) *client.Client {
	if req.ProviderData == nil {
		return keep // provider not configured yet; the framework calls again later
	}
	data, ok := req.ProviderData.(*ProviderData)
	if !ok {
		resp.Diagnostics.AddError(notConfiguredSummary,
			fmt.Sprintf("Unerwarteter Provider-Datentyp: %T", req.ProviderData))
		return keep
	}
	return data.Client
}
