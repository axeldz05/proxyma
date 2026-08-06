package proxyma_bind

import (
	"encoding/json"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveTaskResultPathFromResultPath(t *testing.T) {
	t.Parallel()
	jsonStr := `{"outputs":{"result_path":"/tmp/out.pdf","output_hash":"abc"}}`
	require.Equal(t, "/tmp/out.pdf", ResolveTaskResultPath(jsonStr))
}

func TestResolveTaskResultPathFromNestedData(t *testing.T) {
	t.Parallel()
	jsonStr := `{"data":{"outputs":{"output_path":"/tmp/nested.bin"}}}`
	require.Equal(t, "/tmp/nested.bin", ResolveTaskResultPath(jsonStr))
}

func TestResolveTaskResultPathFromOutputHash(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	StopNode()
	SetStoragePath(dir)
	jsonStr := `{"outputs":{"output_hash":"deadbeef","file":"vfs://deadbeef"}}`
	got := ResolveTaskResultPath(jsonStr)
	require.Equal(t, filepath.Join(dir, "deadbeef"), got)
}

func TestResolveTaskResultPathInvalidJSON(t *testing.T) {
	t.Parallel()
	require.Empty(t, ResolveTaskResultPath("{bad"))
}

func TestBindErrorHelpers(t *testing.T) {
	t.Parallel()
	require.False(t, IsBindError(""))
	require.False(t, IsBindError(`{"message":"ok"}`))
	errJSON := BindErrorJSON(errString("boom"))
	require.True(t, IsBindError(errJSON))
	require.Equal(t, "boom", ParseBindError(errJSON))
}

func TestAddServiceOfflineWritesServicesJSON(t *testing.T) {
	StopNode()
	dir := t.TempDir()
	SetStoragePath(dir)

	res := AddService("offline-ocr", "script", "python3 main.py", "desc", "file:file", "", "")
	require.False(t, IsBindError(res), res)

	svcs, err := compute.LoadServicesMap(dir)
	require.NoError(t, err)
	svc, ok := svcs["offline-ocr"]
	require.True(t, ok)
	require.Equal(t, protocol.ServiceTypeScript, svc.Type)
	require.Equal(t, "python3 main.py", svc.Exec)

	res = RemoveService("offline-ocr")
	require.False(t, IsBindError(res), res)
	svcs, err = compute.LoadServicesMap(dir)
	require.NoError(t, err)
	_, ok = svcs["offline-ocr"]
	require.False(t, ok)
}

func TestLookupServiceSchemaOffline(t *testing.T) {
	StopNode()
	dir := t.TempDir()
	SetStoragePath(dir)

	require.False(t, IsBindError(AddService("schema-svc", "exec", "/bin/true", "hello", "x:string", "", "")))
	schemaJSON := GetServiceSchema("schema-svc")
	require.False(t, IsBindError(schemaJSON), schemaJSON)

	var schema protocol.ServiceSchema
	require.NoError(t, json.Unmarshal([]byte(schemaJSON), &schema))
	require.Equal(t, "schema-svc", schema.Name)
	require.Equal(t, "hello", schema.Description)
}

type errString string

func (e errString) Error() string { return string(e) }
