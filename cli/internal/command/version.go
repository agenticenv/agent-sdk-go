package command

import "fmt"

// Version is the CLI version string, injected by the root command via Bind
// so it stays a single source of truth set in main.go (and stamped by
// GoReleaser at build time).
type Version string

// VersionCmd is `agctl version`.
type VersionCmd struct{}

func (VersionCmd) Run(v Version) error {
	fmt.Println(string(v))
	return nil
}
