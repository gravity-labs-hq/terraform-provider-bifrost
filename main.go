// terraform-provider-bifrost: provider de Terraform para Bifröst.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/gravity-labs-hq/terraform-provider-bifrost/internal/provider"
)

// version la fija goreleaser (-X main.version=...).
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "arranca el provider con soporte de depuración (delve)")
	flag.Parse()
	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/gravity-labs-hq/bifrost",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
