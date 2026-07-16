package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/langshift/lites/internal/capacity/referenceassets"
)

func main() {
	output := flag.String("output", "", "absolute new reference release definition file")
	host := flag.String("provider-host", "", "public TLS Provider hostname")
	secretRef := flag.String("credential-secret-ref", "", "Vault credential reference")
	secretVersion := flag.String("credential-secret-version", "", "pinned Vault secret version")
	runtimeImage := flag.String("runtime-image", "", "signed reference Runtime image at sha256 digest")
	flag.Parse()
	if flag.NArg() != 0 || !filepath.IsAbs(*output) {
		fatal(referenceassets.ErrInvalid)
	}
	definitions, err := referenceassets.Build(referenceassets.Config{ProviderHost: *host, CredentialSecretRef: *secretRef, CredentialSecretVersion: *secretVersion, RuntimeImage: *runtimeImage})
	if err == nil {
		err = referenceassets.Write(*output, definitions)
	}
	if err != nil {
		fatal(err)
	}
	fmt.Printf("Stage 3 reference release definition: profiles=5 output=%s\n", *output)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
