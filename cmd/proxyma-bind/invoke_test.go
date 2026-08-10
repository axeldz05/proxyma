package proxyma_bind

import (
	"encoding/json"
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
		"inputs": "a=1,b=true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["service"] != "clip" {
		t.Fatalf("service=%q", out["service"])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out["payload"]), &payload); err != nil {
		t.Fatalf("payload=%q err=%v", out["payload"], err)
	}
	if payload["a"] != float64(1) || payload["b"] != true {
		t.Fatalf("typed payload=%#v", payload)
	}

	out2, err := NormalizeActionArgs("service", "run", map[string]string{
		"service": "svc",
		"name":    "ignored",
		"payload": `{"x":1}`,
		"inputs":  "x=2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out2["service"] != "svc" || out2["payload"] != `{"x":1}` {
		t.Fatalf("priority service/payload: %#v", out2)
	}

	out3, err := NormalizeActionArgs("service", "run", map[string]string{
		"id":    "from-id",
		"input": "/tmp/f",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out3["service"] != "from-id" || out3["name"] != "from-id" {
		t.Fatalf("id coalesce: %#v", out3)
	}
	var p3 map[string]any
	if err := json.Unmarshal([]byte(out3["payload"]), &p3); err != nil {
		t.Fatal(err)
	}
	if p3["input_path"] != "/tmp/f" {
		t.Fatalf("input shorthand: %#v", p3)
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
