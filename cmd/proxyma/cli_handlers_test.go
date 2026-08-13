package main

import (
	"testing"

	"proxyma/shared/uischema"

	"github.com/spf13/cobra"
)

func TestFlagArgsMapReadsRequestedFlags(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("name", "default", "")
	cmd.Flags().String("strategy", "", "")
	if err := cmd.Flags().Set("name", "demo"); err != nil {
		t.Fatal(err)
	}

	got := flagArgsMap(cmd, "name", "strategy")
	if got["name"] != "demo" || got["strategy"] != "" {
		t.Fatalf("flag args = %#v", got)
	}
}

func TestVisibleActionsEscapeXorUnix(t *testing.T) {
	escapes := CLIEscapeKeys()
	for _, d := range uischema.VisibleRegistry("cli") {
		for _, a := range d.Actions {
			_, hasEscape := escapes[a.Key()]
			hasUnix := a.UnixAction != ""
			if !hasEscape && !hasUnix {
				t.Errorf("visible action %s has neither cliEscape nor UnixAction", a.Key())
			}
			// Escapes may also have UnixAction (e.g. storage.open, service.run) — that is fine.
		}
	}
}

func TestCLIRegistryIncludesHiddenEscapes(t *testing.T) {
	want := map[string]bool{"cluster.join": false, "service.edit_pipeline": false}
	for _, d := range cliRegistry() {
		for _, a := range d.Actions {
			if _, ok := want[a.Key()]; ok {
				want[a.Key()] = true
			}
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("cliRegistry missing hidden escape %s", k)
		}
	}
}
