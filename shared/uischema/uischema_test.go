package uischema

import (
	"testing"

	"proxyma/internal/protocol"
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
			if a.Name == "detail" || a.Name == "stream" || a.Name == "validate_pipeline" ||
				a.Name == "join" || a.Name == "edit_pipeline" {
				t.Errorf("internal/escape-only action %s should not be visible", a.Key())
			}
		}
	}
}

func TestHiddenEscapeActionsStillFindable(t *testing.T) {
	for _, key := range []struct{ domain, name string }{
		{"cluster", "join"},
		{"service", "edit_pipeline"},
	} {
		a, ok := FindAction(key.domain, key.name)
		if !ok {
			t.Fatalf("expected %s.%s", key.domain, key.name)
		}
		if !a.Hidden {
			t.Errorf("%s.%s should be Hidden", key.domain, key.name)
		}
		if a.UnixAction != "" {
			t.Errorf("%s.%s should have empty UnixAction", key.domain, key.name)
		}
	}
}

func TestMustUnixAction(t *testing.T) {
	if MustUnixAction("peers", "list") != "peers" {
		t.Fatal("unexpected peers.list unix action")
	}
}

func TestValidateActionArgs(t *testing.T) {
	a, ok := FindAction("service", "run")
	if !ok {
		t.Fatal("expected service.run")
	}
	_, err := ValidateActionArgs(a, map[string]string{})
	if err == nil {
		t.Fatal("expected missing required")
	}
	out, err := ValidateActionArgs(a, map[string]string{"name": "ocr", "strategy": "fastest"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out["name"] != "ocr" {
		t.Fatalf("name=%q", out["name"])
	}
	_, err = ValidateActionArgs(a, map[string]string{"name": "ocr", "strategy": "bogus"})
	if err == nil {
		t.Fatal("expected invalid strategy")
	}
	_, err = ValidateActionArgs(a, map[string]string{"name": "ocr", "strategy": protocol.StrategyCheapest})
	if err != nil {
		t.Fatalf("URN strategy should be accepted via normalize: %v", err)
	}
}
