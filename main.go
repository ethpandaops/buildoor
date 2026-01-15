// Package main provides the entry point for the buildoor application.
package main

import (
	"os"

	"github.com/ethpandaops/buildoor/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
