package main

import (
	"testing"

	"github.com/alecthomas/kong"
)

func TestKongCLIRegisters(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("agctl"),
		kong.Vars{"version": "test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"version"},
		{"chat"},
		{"run", "--prompt", "hi"},
		{"config", "path"},
		{"config", "show"},
		{"config", "edit"},
	} {
		if _, err := parser.Parse(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
	}
}
