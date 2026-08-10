package proxyma_bind

import (
	"testing"

	"proxyma/internal/server"
	"proxyma/shared/uischema"
)

func TestNormalizeActionArgsUpload(t *testing.T) {
	out, err := NormalizeActionArgs("storage", "upload", map[string]string{"path": "/tmp/foo.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out["name"] != "foo.txt" {
		t.Fatalf("name=%q", out["name"])
	}
}

func TestNormalizeActionArgsRun(t *testing.T) {
	out, err := NormalizeActionArgs("service", "run", map[string]string{
		"name":   "clip",
		"inputs": "a=1,b=2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["service"] != "clip" {
		t.Fatalf("service=%q", out["service"])
	}
	if out["payload"] == "" || out["payload"][0] != '{' {
		t.Fatalf("payload=%q", out["payload"])
	}
}

func TestUnaryUnixActionsHaveHandlers(t *testing.T) {
	streamUA := uischema.MustUnixAction("service", "stream")
	for ua, key := range uischema.AllUnixActions() {
		if ua == streamUA {
			continue
		}
		if !server.HasUnixUnary(ua) {
			t.Errorf("unix action %q (%s) missing unary handler", ua, key)
		}
	}
}
