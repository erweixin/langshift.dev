package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/langshift/lites/internal/releaseassets"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("lites-release-assets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configuration := flags.String("config", "", "absolute path to the reviewed release definitions")
	output := flags.String("output", "", "absolute path to a new bundle directory")
	sourceCommit := flags.String("source-commit", "", "exact 40-character source commit")
	sourceDateEpoch := flags.String("source-date-epoch", "", "positive source commit timestamp in Unix seconds")
	if err := flags.Parse(arguments); err != nil {
		return releaseassets.ErrInvalid
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*configuration) || !filepath.IsAbs(*output) {
		return releaseassets.ErrInvalid
	}
	epoch, err := strconv.ParseInt(*sourceDateEpoch, 10, 64)
	if err != nil || epoch <= 0 {
		return releaseassets.ErrInvalid
	}
	generatedAt := time.Unix(epoch, 0).UTC()
	definitions, err := releaseassets.LoadDefinitions(*configuration)
	if err != nil {
		return err
	}
	bundle, err := releaseassets.Build(definitions, *sourceCommit, generatedAt)
	if err != nil {
		return err
	}
	if err = releaseassets.Write(*output, bundle); err != nil {
		return err
	}
	if _, err = fmt.Fprintf(stdout, "Agent release assets: %s output=%s\n", bundle.Manifest, *output); err != nil {
		return errors.New("write release result")
	}
	return nil
}
