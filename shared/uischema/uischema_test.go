package uischema

import (
	"testing"
)

func TestFindActionAndUnixActionFor(t *testing.T) {
	a, ok := FindAction("storage", "list")
	if !ok {
		t.Fatal("expected storage.list")
	}
	if a.UnixAction != "vfs_list" {
		t.Fatalf("UnixAction=%q want vfs_list", a.UnixAction)
	}
	ua, ok := UnixActionFor("storage", "list")
	if !ok || ua != "vfs_list" {
		t.Fatalf("UnixActionFor=%q ok=%v", ua, ok)
	}
	if _, ok := UnixActionFor("cluster", "join"); ok {
		t.Fatal("cluster.join should have no unix action")
	}
}

func TestApplyDefaultsAndMissingRequired(t *testing.T) {
	a, ok := FindAction("cluster", "join")
	if !ok {
		t.Fatal("expected cluster.join")
	}
	args := ApplyDefaults(a, map[string]string{"token": "tok"})
	if args["port"] == "" {
		t.Fatal("expected default port")
	}
	missing := MissingRequired(a, map[string]string{})
	if len(missing) == 0 {
		t.Fatal("expected missing required params")
	}
}

func TestVisibleRegistryHidesInternal(t *testing.T) {
	for _, d := range VisibleRegistry("cli") {
		for _, a := range d.Actions {
			if a.Hidden {
				t.Errorf("visible registry leaked hidden action %s", a.Key())
			}
			if a.Name == "detail" || a.Name == "stream" || a.Name == "validate_pipeline" {
				t.Errorf("internal action %s should not be visible", a.Key())
			}
		}
	}
}

func TestMustUnixAction(t *testing.T) {
	if MustUnixAction("peers", "list") != "peers" {
		t.Fatal("unexpected peers.list unix action")
	}
}
