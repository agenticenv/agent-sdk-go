package main

import (
	_ "embed"
	"fmt"
	"os"
)

// version is set at link time by GoReleaser (release) or left as "dev" for local builds.
var version = "dev"

//go:embed default.yaml
var defaultConfigYAML []byte

func main() {
	if err := Execute(version, defaultConfigYAML); err != nil {
		fmt.Fprintf(os.Stderr, "agctl: %v\n", err)
		os.Exit(1)
	}
}
