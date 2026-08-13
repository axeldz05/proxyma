package proxyma_bind

import (
	"encoding/json"
	"strings"
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

func TestNormalizeActionArgsAddPipelineRequiresSchema(t *testing.T) {
	_, err := NormalizeActionArgs("service", "add_pipeline", map[string]string{"id": "p1"})
	if err == nil || !strings.Contains(err.Error(), "schema or schema-file required") {
		t.Fatalf("expected schema or schema-file required, got %v", err)
	}

	raw := `{"id":"p1","version":1,"steps":[{"id":"s1","service":"echo"}]}`
	out, err := NormalizeActionArgs("service", "add_pipeline", map[string]string{
		"id":     "p1",
		"schema": raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["schema"] == "" {
		t.Fatal("expected schema populated")
	}
}

func TestDecodeActionArgsJSONPreservesPrimitiveAndStructuredValues(t *testing.T) {
	args, err := decodeActionArgsJSON(`{"name":"demo","enabled":true,"count":2,"payload":{"x":1}}`)
	if err != nil {
		t.Fatal(err)
	}
	if args["name"] != "demo" || args["enabled"] != "true" || args["count"] != "2" || args["payload"] != `{"x":1}` {
		t.Fatalf("decoded args = %#v", args)
	}
}

func TestProjectActionRowsJSONUsesUISchemaFormatting(t *testing.T) {
	raw := ProjectActionRowsJSON(
		"storage",
		"list",
		`[{"name":"blob.bin","version":1,"size":2048,"subscribed":true,"hasLocal":false,"deleted":false,"hash":"abc"}]`,
	)
	if IsBindError(raw) {
		t.Fatalf("ProjectActionRowsJSON returned error: %s", raw)
	}
	var table projectedActionTable
	if err := json.Unmarshal([]byte(raw), &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 1 || len(table.Rows[0]) < 3 || table.Rows[0][2] != "2.00 KB" {
		t.Fatalf("projected table = %#v", table)
	}
}

func TestProjectActionRowsJSONSupportsScalarLists(t *testing.T) {
	raw := ProjectActionRowsJSON("service", "discover", `["echo","resize"]`)
	if IsBindError(raw) {
		t.Fatalf("ProjectActionRowsJSON returned error: %s", raw)
	}
	var table projectedActionTable
	if err := json.Unmarshal([]byte(raw), &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 2 || table.Rows[0][0] != "echo" || table.Rows[1][0] != "resize" {
		t.Fatalf("projected table = %#v", table)
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

func TestOfflineHooksLookup(t *testing.T) {
	for _, key := range []string{"service.add", "service.remove", "service.detail"} {
		if _, ok := offlineHooks[key]; !ok {
			t.Errorf("missing offline hook %q", key)
		}
	}
	if offlineHookFor("storage", "list", nil) != nil {
		t.Fatal("storage.list must not have offline arm")
	}
	if offlineHookFor("service", "add", map[string]string{}) == nil {
		t.Fatal("service.add must resolve offline arm")
	}
}

func TestInvokeDomainActionPreparedSkipsRevalidate(t *testing.T) {
	// Prepared does not call ValidateActionArgs — missing required args reach dispatch
	// and fail as bind/daemon errors, not as validation errors from uischema.
	raw := InvokeDomainActionPrepared("service", "remove", map[string]string{})
	if !IsBindError(raw) {
		t.Fatalf("expected bind error without name, got %s", raw)
	}
	if ParseBindError(raw) == "" {
		t.Fatal("expected non-empty bind error")
	}
	// Full L3 path surfaces validation before dispatch.
	raw2 := InvokeDomainAction("service", "remove", map[string]string{})
	if !IsBindError(raw2) {
		t.Fatalf("expected validation error, got %s", raw2)
	}
	msg := strings.ToLower(ParseBindError(raw2))
	if !strings.Contains(msg, "required") && !strings.Contains(msg, "name") {
		t.Fatalf("expected required/name validation, got %q", msg)
	}
}
