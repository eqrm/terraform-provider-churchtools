package provider

import (
	"fmt"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

// notConfigured guards every CRUD body against a nil client.
//
// configureClient returns `keep` when the framework calls Configure with no
// provider data -- which is what happens when the provider's own Configure
// deferred on an unknown credential -- so a fresh resource instance can reach
// Create/Read/Update holding nil. A nil *client.Client dereferences on its
// first field access and panics the plugin process, which Terraform reports as
// a crash with no cause attached. The client refuses a nil receiver too, but
// only the resource knows the German summary an operator should see.
//
// Delete and ImportState are absent on purpose: neither touches the client.
func notConfigured(c *client.Client, diags *diag.Diagnostics) bool {
	if c != nil {
		return false
	}
	diags.AddError(notConfiguredSummary, notConfiguredDetail)
	return true
}
