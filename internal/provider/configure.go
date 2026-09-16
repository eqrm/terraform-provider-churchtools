package provider

import (
	"fmt"

	"github.com/eqrm/terraform-provider-churchtools/internal/client"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// configureClient is the one Configure body every resource shares. Keeping it
// in one place means a change to how the provider is wired cannot be applied
// to four resources and forgotten on the fifth.
func configureClient(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *client.Client {
	if req.ProviderData == nil {
		return nil // provider not configured yet; the framework calls again later
	}
	data, ok := req.ProviderData.(*ProviderData)
	if !ok {
		resp.Diagnostics.AddError(notConfiguredSummary,
			fmt.Sprintf("Unerwarteter Provider-Datentyp: %T", req.ProviderData))
		return nil
	}
	return client.New(data.Host, data.Token)
}
