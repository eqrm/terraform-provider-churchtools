package main

import (
	"context"
	"log"

	"github.com/eqrm/terraform-provider-churchtools/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var version = "dev"

func main() {
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.opentofu.org/eqrm/churchtools",
	})
	if err != nil {
		log.Fatal(err)
	}
}
// probe
