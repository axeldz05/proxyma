package server

import (
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"proxyma/shared/uischema"
)

func TestRawUnixRequestsValidateRequiredTypesAndOptionsBeforeDispatch(t *testing.T) {
	const unixAction = "contract_validation"
	originalRegistry := uischema.Registry
	originalHandler, hadOriginalHandler := unixHandlers[unixAction]
	t.Cleanup(func() {
		uischema.Registry = originalRegistry
		if hadOriginalHandler {
			unixHandlers[unixAction] = originalHandler
		} else {
			delete(unixHandlers, unixAction)
		}
	})

	uischema.Registry = append(uischema.Registry, uischema.DomainDetail{
		Name:  "contract",
		Title: "Contract",
		Actions: []uischema.ActionDetail{{
			Domain:     "contract",
			Name:       "validate",
			Title:      "Validate",
			OutputType: "json",
			UnixAction: unixAction,
			Parameters: []uischema.ParameterDetail{
				{Name: "name", Type: "string", Required: true},
				{Name: "count", Type: "int"},
				{Name: "enabled", Type: "bool"},
				{Name: "mode", Type: "string", Options: []string{"safe", "fast"}},
			},
		}},
	})

	var calls atomic.Int32
	unixHandlers[unixAction] = UnixActionHandler{
		Unary: func(_ *Server, args map[string]string) (any, error) {
			calls.Add(1)
			return args, nil
		},
	}

	tests := []struct {
		name string
		args map[string]string
		want string
	}{
		{name: "required", args: map[string]string{}, want: "required"},
		{name: "integer type", args: map[string]string{"name": "x", "count": "NaN"}, want: "count"},
		{name: "boolean type", args: map[string]string{"name": "x", "enabled": "maybe"}, want: "enabled"},
		{name: "options", args: map[string]string{"name": "x", "mode": "unsafe"}, want: "mode"},
	}
	for _, test := range tests {
		resp := sendRawUnixContractRequest(t, &Server{}, protocol.UnixRequest{
			Action: unixAction,
			Args:   test.args,
		})
		if resp.Success {
			t.Errorf("%s request unexpectedly dispatched: %#v", test.name, resp)
		}
		if !strings.Contains(strings.ToLower(resp.Error), strings.ToLower(test.want)) {
			t.Errorf("%s error = %q, want %q", test.name, resp.Error, test.want)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("handler called %d time(s) for invalid raw requests", got)
	}

	resp := sendRawUnixContractRequest(t, &Server{}, protocol.UnixRequest{
		Action: unixAction,
		Args: map[string]string{
			"name":    "valid",
			"count":   "2",
			"enabled": "true",
			"mode":    "safe",
		},
	})
	if !resp.Success {
		t.Fatalf("valid raw request rejected: %s", resp.Error)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("valid handler call count = %d, want 1", got)
	}
}

func TestRawUnixPipelineArgsUseSharedNormalization(t *testing.T) {
	rawSchema := `{"id":"old","version":1,"steps":[{"id":"step","service":"echo"}]}`
	args, err := validateUnixArgs(
		uischema.MustUnixAction("service", "add_pipeline"),
		map[string]string{"id": "effective", "schema": rawSchema},
	)
	if err != nil {
		t.Fatal(err)
	}
	var schema protocol.PipelineSchema
	if err := json.Unmarshal([]byte(args["schema"]), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.ID != "effective" {
		t.Fatalf("normalized pipeline ID = %q, want effective", schema.ID)
	}
}

func TestRawUnixJSONDecodeReturnsStructuredErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "whitespace malformed", raw: "   {bad", want: "invalid JSON request"},
		{name: "non object", raw: "[]", want: "invalid JSON request"},
		{name: "oversized", raw: `{"action":"` + strings.Repeat("x", maxUnixRequestBytes) + `"}`, want: "exceeds"},
	}
	for _, test := range tests {
		resp := sendRawUnixContractBytes(t, &Server{}, []byte(test.raw))
		if resp.Success || !strings.Contains(resp.Error, test.want) {
			t.Errorf("%s response = %#v, want structured %q error", test.name, resp, test.want)
		}
	}
}

func TestRawUnixVFSListSurfacesDatabaseFailure(t *testing.T) {
	t.Parallel()
	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	if err := srv.Storage.Close(); err != nil {
		t.Fatal(err)
	}

	resp := sendRawUnixContractRequest(t, srv, protocol.UnixRequest{
		Action: uischema.MustUnixAction("storage", "list"),
	})
	if resp.Success {
		t.Fatalf("closed VFS database returned empty success: %s", resp.Data)
	}
	if !strings.Contains(strings.ToLower(resp.Error), "vfs") {
		t.Fatalf("VFS list error = %q, want database failure context", resp.Error)
	}
}

func sendRawUnixContractBytes(t *testing.T, srv *Server, raw []byte) protocol.UnixResponse {
	t.Helper()
	client, daemon := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleUnixConnection(daemon)
	}()
	writeDone := make(chan error, 1)
	go func() {
		_, err := client.Write(raw)
		writeDone <- err
	}()
	var resp protocol.UnixResponse
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-writeDone
	<-done
	return resp
}

func TestRawUnixVFSReadSurfacesDatabaseFailure(t *testing.T) {
	t.Parallel()
	srv := newLifecycleServer(t, &testutil.MockPeerClient{})
	if err := srv.Storage.Close(); err != nil {
		t.Fatal(err)
	}

	resp := sendRawUnixContractRequest(t, srv, protocol.UnixRequest{
		Action: uischema.MustUnixAction("storage", "open"),
		Args:   map[string]string{"name": "missing.txt"},
	})
	if resp.Success {
		t.Fatalf("closed VFS database returned read success: %s", resp.Data)
	}
	errorText := strings.ToLower(resp.Error)
	if !strings.Contains(errorText, "vfs metadata") && !strings.Contains(errorText, "database") {
		t.Fatalf("VFS read error = %q, want underlying database failure", resp.Error)
	}
	if strings.Contains(errorText, "not found") {
		t.Fatalf("database failure was misreported as absence: %q", resp.Error)
	}
}

func sendRawUnixContractRequest(
	t *testing.T,
	srv *Server,
	req protocol.UnixRequest,
) protocol.UnixResponse {
	t.Helper()
	client, daemon := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleUnixConnection(daemon)
	}()
	if err := json.NewEncoder(client).Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp protocol.UnixResponse
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	<-done
	return resp
}
