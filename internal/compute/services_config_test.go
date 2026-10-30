package compute

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"proxyma/internal/protocol"

	"github.com/stretchr/testify/require"
)

// The protocol type table and the compute builder table must stay aligned: a type
// declared without a builder would only fail at service registration time.
func TestEveryServiceTypeHasBuilder(t *testing.T) {
	for _, typ := range protocol.KnownServiceTypes() {
		if _, ok := serviceTypeBuilders[typ]; !ok {
			t.Errorf("service type %q has no handler builder", typ)
		}
	}

	known := make(map[protocol.ServiceType]bool, len(protocol.KnownServiceTypes()))
	protocol.PopulateMap(known, protocol.KnownServiceTypes(), true)
	for typ := range serviceTypeBuilders {
		if !known[typ] {
			t.Errorf("builder registered for %q, which is not a canonical protocol type", typ)
		}
		if typ.Normalize() != typ {
			t.Errorf("builder keyed by alias %q; key it by the canonical type %q", typ, typ.Normalize())
		}
	}
}

func TestRequireHTTPExec(t *testing.T) {
	if err := requireHTTPExec("https://host/x", protocol.ServiceTypeWebRTC, "signaling URL"); err != nil {
		t.Errorf("https exec must be accepted: %v", err)
	}
	if err := requireHTTPExec("/usr/bin/thing", protocol.ServiceTypeServerStream, "exec URL"); err == nil {
		t.Error("local command must be rejected for http-only types")
	}
	if err := requireHTTPExec("/usr/bin/thing", protocol.ServiceTypeHTTP, "webhook URL"); err == nil {
		t.Error("local command must be rejected for http service type")
	}
	if err := requireHTTPExec("http://localhost:8080/webhook", protocol.ServiceTypeHTTP, "webhook URL"); err != nil {
		t.Errorf("http exec must be accepted: %v", err)
	}

	for _, typ := range []protocol.ServiceType{protocol.ServiceTypeHTTP, protocol.ServiceTypeGRPC} {
		if _, err := BuildHandler(typ, "/bin/echo"); err == nil {
			t.Errorf("type %q must reject non-http exec via BuildHandler", typ)
		}
	}
}

func TestBuildHandlerRejectsUnknownType(t *testing.T) {
	if _, err := BuildHandler(protocol.ServiceType("quantum"), "x"); err == nil {
		t.Error("unknown service type must be rejected")
	}
}

func TestSchemaFileDefaultsAndPersistsNormalizedExecType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{
		"description": "schema-only service",
		"parameters": {"input": {"type": "string", "required": true}}
	}`), 0o644))

	name, service, err := BuildLocalServiceFromArgs(
		"schema.exec",
		"",
		"printf '{}'",
		"",
		"",
		"",
		schemaPath,
	)
	require.NoError(t, err)
	require.Equal(t, "schema.exec", name)
	require.Equal(t, protocol.ServiceTypeExec, service.Type)
	require.Equal(t, protocol.ServiceTypeExec, service.Schema.Type)
	require.NoError(t, UpsertLocalService(dir, name, service))

	services, err := LoadServicesMap(dir)
	require.NoError(t, err)
	require.Equal(t, protocol.ServiceTypeExec, services[name].Type)
	require.Equal(t, protocol.ServiceTypeExec, services[name].Schema.Type)
}

func TestSchemaFileRejectsHandlerInvalidFieldsBeforePersistence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(`{
		"description": "invalid transport",
		"parameters": {}
	}`), 0o644))

	_, _, err := BuildLocalServiceFromArgs(
		"bad-stream",
		string(protocol.ServiceTypeServerStream),
		"/usr/bin/local-command",
		"",
		"",
		"",
		schemaPath,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires http(s)")

	_, _, err = BuildLocalServiceFromArgs("missing-exec", "", "", "", "", "", schemaPath)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exec")

	_, statErr := os.Stat(ServicesFilePath(dir))
	require.True(t, os.IsNotExist(statErr), "rejected schema must not be acknowledged on disk")
}

func TestUpsertLocalServiceValidatesAndNormalizesDirectCallers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	service := protocol.LocalService{
		Type: protocol.ServiceTypeHTTPBidi,
		Exec: "run-stream",
		Schema: protocol.ServiceSchema{
			Name:       "alias",
			Type:       protocol.ServiceTypeBidiStream,
			Parameters: map[string]protocol.ServiceParameter{},
		},
	}
	require.NoError(t, UpsertLocalService(dir, "alias", service))

	services, err := LoadServicesMap(dir)
	require.NoError(t, err)
	require.Equal(t, protocol.ServiceTypeHTTPBidi, services["alias"].Type)
	require.Equal(t, protocol.ServiceTypeHTTPBidi, services["alias"].Schema.Type)

	err = UpsertLocalService(dir, "broken", protocol.LocalService{
		Type:   protocol.ServiceType("unknown"),
		Exec:   "x",
		Schema: protocol.ServiceSchema{Name: "broken"},
	})
	require.Error(t, err)
	services, loadErr := LoadServicesMap(dir)
	require.NoError(t, loadErr)
	require.NotContains(t, services, "broken")
}

func TestConcurrentServiceUpsertsPreserveEveryAcknowledgedEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const writers = 96
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup

	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			name := fmt.Sprintf("service-%03d", i)
			errs <- UpsertLocalService(dir, name, protocol.LocalService{
				Type: protocol.ServiceTypeScreen,
				Exec: "fake",
				Schema: protocol.ServiceSchema{
					Name:       name,
					Type:       protocol.ServiceTypeScreen,
					Parameters: map[string]protocol.ServiceParameter{},
				},
			})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	services, err := LoadServicesMap(dir)
	require.NoError(t, err)
	require.Len(t, services, writers)
	for i := range writers {
		require.Contains(t, services, fmt.Sprintf("service-%03d", i))
	}
}

func TestServicesFileUsesCrossProcessAdvisoryLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	unlock, err := lockServicesFile(dir, true)
	require.NoError(t, err)

	started := make(chan struct{})
	acquired := make(chan struct{})
	releaseSecond := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		close(started)
		secondUnlock, err := lockServicesFile(dir, true)
		if err != nil {
			errCh <- err
			return
		}
		close(acquired)
		<-releaseSecond
		errCh <- secondUnlock()
	}()

	<-started
	select {
	case <-acquired:
		t.Fatal("a second process/file descriptor must not enter the same write critical section while held")
	case err := <-errCh:
		t.Fatalf("unexpected error acquiring second lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, unlock())

	select {
	case <-acquired:
	case err := <-errCh:
		t.Fatalf("failed acquiring second lock after release: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second lock to acquire after release")
	}

	close(releaseSecond)
	require.NoError(t, <-errCh)
}

func TestServicesFileSharedLockAllowsConcurrentReaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	unlock1, err := lockServicesFile(dir, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, unlock1()) }()

	unlock2, err := lockServicesFile(dir, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, unlock2()) }()
}

func TestHTTPAndLegacyServiceTypesInServicesConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	testCases := []struct {
		name         string
		serviceType  protocol.ServiceType
		exec         string
		expectedType protocol.ServiceType
	}{
		{"http-service", protocol.ServiceTypeHTTP, "http://localhost:8080/do", protocol.ServiceTypeHTTP},
		{"grpc-alias-service", protocol.ServiceTypeGRPC, "http://localhost:8080/do", protocol.ServiceTypeHTTP},
		{"http-bidi-service", protocol.ServiceTypeHTTPBidi, "echo bidi", protocol.ServiceTypeHTTPBidi},
		{"grpc-bidi-alias", protocol.ServiceTypeGRPCBidi, "echo bidi", protocol.ServiceTypeHTTPBidi},
		{"bidi-grpc-alias", protocol.ServiceTypeBidiGRPC, "echo bidi", protocol.ServiceTypeHTTPBidi},
	}

	for _, tc := range testCases {
		svc := protocol.LocalService{
			Type: tc.serviceType,
			Exec: tc.exec,
			Schema: protocol.ServiceSchema{
				Name:       tc.name,
				Type:       tc.serviceType,
				Parameters: map[string]protocol.ServiceParameter{},
			},
		}
		require.NoError(t, UpsertLocalService(dir, tc.name, svc))
	}

	loaded, err := LoadServicesMap(dir)
	require.NoError(t, err)
	for _, tc := range testCases {
		s, ok := loaded[tc.name]
		require.True(t, ok, "service %s should exist in loaded services", tc.name)
		require.Equal(t, tc.expectedType, s.Type)
		require.Equal(t, tc.expectedType, s.Schema.Type)

		handler, err := BuildHandler(s.Type, s.Exec)
		require.NoError(t, err, "handler for %s must build cleanly", tc.name)
		require.NotNil(t, handler)
	}
}
