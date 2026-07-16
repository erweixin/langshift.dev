package main

import (
	"fmt"
	"os"

	"github.com/langshift/lites/internal/capacity/referencetool"
)

func main() {
	if err := referencetool.Run(os.Args[1:], os.Getenv("LITES_REQUEST_ID"), os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
