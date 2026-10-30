package storage_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"

	"github.com/stretchr/testify/require"
)

func TestHandleUpload_RejectsPayloadOver10MB(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-upload-limit")
	engine := testutil.NewStorageEngine(t, cfg)

	// Prepare multipart request exceeding 10 MB (10 MB + 1024 bytes)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "large.bin")
	require.NoError(t, err)

	// Write 10 MB + 1 KB
	chunk := make([]byte, 1024*1024)
	for i := 0; i < 10; i++ {
		_, err := part.Write(chunk)
		require.NoError(t, err)
	}
	_, err = part.Write(make([]byte, 1024))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	engine.HandleUpload(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errPayload map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errPayload))
	require.Equal(t, "Unable to parse form", errPayload["error"])
}

func TestHandleUpload_MissingFileField(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-upload-missing-file")
	engine := testutil.NewStorageEngine(t, cfg)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormField("other_field")
	require.NoError(t, err)
	_, err = part.Write([]byte("some data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	engine.HandleUpload(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errPayload map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errPayload))
	require.Equal(t, "Error retrieving file", errPayload["error"])
}

func TestHandleUpload_InvalidForm(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-upload-invalid-form")
	engine := testutil.NewStorageEngine(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("plain text not multipart"))
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()

	engine.HandleUpload(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errPayload map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errPayload))
	require.Equal(t, "Unable to parse form", errPayload["error"])
}

func TestHandleUpload_Success(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-upload-success")
	engine := testutil.NewStorageEngine(t, cfg)

	payload := []byte("hello storage upload world")
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "uploaded.txt")
	require.NoError(t, err)
	_, err = part.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	engine.HandleUpload(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var respPayload map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&respPayload))
	require.Equal(t, "Blob uploaded successfully", respPayload["message"])

	// Verify persistence in VFS
	meta, exists, err := engine.GetFileMetaE("uploaded.txt")
	require.NoError(t, err)
	require.True(t, exists)
	require.False(t, meta.Deleted)
	require.Equal(t, int64(len(payload)), meta.Size)

	hasBlob, err := engine.HasPhysicalBlob(meta.Hash)
	require.NoError(t, err)
	require.True(t, hasBlob)
}

func TestHandleUpload_SaveErrorOnClosedEngine(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-upload-closed")
	engine := testutil.NewStorageEngine(t, cfg)
	require.NoError(t, engine.Close())

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "closed.txt")
	require.NoError(t, err)
	_, err = part.Write([]byte("content"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	engine.HandleUpload(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	var errPayload map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errPayload))
	require.Equal(t, "Failed to save file", errPayload["error"])
}

func TestHandleUpload_NilBody(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-upload-nil-body")
	engine := testutil.NewStorageEngine(t, cfg)

	req := httptest.NewRequest(http.MethodPost, "/upload", nil)
	req.Body = nil // Explicitly test nil Body edge case
	w := httptest.NewRecorder()

	engine.HandleUpload(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var errPayload map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errPayload))
	require.Equal(t, "Unable to parse form", errPayload["error"])
}

func TestHandleUpload_EmptyFilename(t *testing.T) {
	t.Parallel()
	cfg := testutil.DefaultConfig(t, "node-upload-empty-filename")
	engine := testutil.NewStorageEngine(t, cfg)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "   ")
	require.NoError(t, err)
	_, err = part.Write([]byte("some data"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	engine.HandleUpload(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var errPayload map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errPayload))
	require.Equal(t, "File name cannot be empty", errPayload["error"])
}

func TestHandleDelete(t *testing.T) {
	t.Parallel()

	t.Run("missing query param returns 400", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "del-missing-param")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodDelete, "/delete", nil)
		w := httptest.NewRecorder()
		engine.HandleDelete(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Contains(t, errPayload["error"], "Missing 'name' query parameter")
	})

	t.Run("non-existent file returns 404", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "del-not-found")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodDelete, "/delete?name=notfound.txt", nil)
		w := httptest.NewRecorder()
		engine.HandleDelete(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Contains(t, errPayload["error"], "file notfound.txt not found")
	})

	t.Run("existing file returns 200 and tombstones file", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "del-success")
		engine := testutil.NewStorageEngine(t, cfg)

		require.NoError(t, engine.SaveLocalFile("to-delete.txt", strings.NewReader("delete me")))

		req := httptest.NewRequest(http.MethodDelete, "/delete?name=to-delete.txt", nil)
		w := httptest.NewRecorder()
		engine.HandleDelete(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var respPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&respPayload))
		require.Equal(t, "File deleted successfully", respPayload["message"])

		meta, exists, err := engine.GetFileMetaE("to-delete.txt")
		require.NoError(t, err)
		require.True(t, exists)
		require.True(t, meta.Deleted)
	})

	t.Run("internal error on closed engine returns 500", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "del-closed")
		engine := testutil.NewStorageEngine(t, cfg)
		require.NoError(t, engine.Close())

		req := httptest.NewRequest(http.MethodDelete, "/delete?name=any.txt", nil)
		w := httptest.NewRecorder()
		engine.HandleDelete(w, req)

		require.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

func TestHandleSubscribe(t *testing.T) {
	t.Parallel()

	t.Run("missing query param returns 400", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "sub-missing-param")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodPost, "/subscribe", nil)
		w := httptest.NewRecorder()
		engine.HandleSubscribe(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Contains(t, errPayload["error"], "Missing 'name' query parameter")
	})

	t.Run("valid subscription returns 200", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "sub-success")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodPost, "/subscribe?name=important.txt", nil)
		w := httptest.NewRecorder()
		engine.HandleSubscribe(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var respPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&respPayload))
		require.Equal(t, "Subscribed to important.txt", respPayload["message"])

		sub, err := engine.IsSubscribedE("important.txt")
		require.NoError(t, err)
		require.True(t, sub)
	})

	t.Run("valid subscription via JSON body returns 200", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "sub-body-success")
		engine := testutil.NewStorageEngine(t, cfg)

		body := strings.NewReader(`{"name":"body-file.txt"}`)
		req := httptest.NewRequest(http.MethodPost, "/subscribe", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.HandleSubscribe(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var respPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&respPayload))
		require.Equal(t, "Subscribed to body-file.txt", respPayload["message"])

		sub, err := engine.IsSubscribedE("body-file.txt")
		require.NoError(t, err)
		require.True(t, sub)
	})

	t.Run("invalid JSON body returns 400", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "sub-invalid-json")
		engine := testutil.NewStorageEngine(t, cfg)

		body := strings.NewReader(`{invalid-json`)
		req := httptest.NewRequest(http.MethodPost, "/subscribe", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.HandleSubscribe(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Equal(t, "Invalid JSON payload", errPayload["error"])
	})

	t.Run("JSON body with empty name returns 400", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "sub-empty-name")
		engine := testutil.NewStorageEngine(t, cfg)

		body := strings.NewReader(`{"name":""}`)
		req := httptest.NewRequest(http.MethodPost, "/subscribe", body)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.HandleSubscribe(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Contains(t, errPayload["error"], "Missing 'name' query parameter")
	})

	t.Run("closed engine returns 500", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "sub-closed")
		engine := testutil.NewStorageEngine(t, cfg)
		require.NoError(t, engine.Close())

		req := httptest.NewRequest(http.MethodPost, "/subscribe?name=important.txt", nil)
		w := httptest.NewRecorder()
		engine.HandleSubscribe(w, req)

		require.Equal(t, http.StatusInternalServerError, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Equal(t, "Failed to persist subscription", errPayload["error"])
	})
}

func TestHandleManifest(t *testing.T) {
	t.Parallel()

	t.Run("successful manifest returns 200 with snapshot JSON", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "manifest-success")
		engine := testutil.NewStorageEngine(t, cfg)

		require.NoError(t, engine.SaveLocalFile("doc1.txt", strings.NewReader("hello doc 1")))
		require.NoError(t, engine.SaveLocalFile("doc2.txt", strings.NewReader("hello doc 2")))

		req := httptest.NewRequest(http.MethodGet, "/manifest", nil)
		w := httptest.NewRecorder()
		engine.HandleManifest(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var snapshot map[string]protocol.IndexEntry
		require.NoError(t, json.NewDecoder(w.Body).Decode(&snapshot))
		require.Contains(t, snapshot, "doc1.txt")
		require.Contains(t, snapshot, "doc2.txt")
		require.Equal(t, int64(len("hello doc 1")), snapshot["doc1.txt"].Size)
		require.Equal(t, int64(len("hello doc 2")), snapshot["doc2.txt"].Size)
	})

	t.Run("empty manifest returns 200 with empty snapshot JSON", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "manifest-empty")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodGet, "/manifest", nil)
		w := httptest.NewRecorder()
		engine.HandleManifest(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var snapshot map[string]protocol.IndexEntry
		require.NoError(t, json.NewDecoder(w.Body).Decode(&snapshot))
		require.Empty(t, snapshot)
	})

	t.Run("closed engine returns 500", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "manifest-closed")
		engine := testutil.NewStorageEngine(t, cfg)
		require.NoError(t, engine.Close())

		req := httptest.NewRequest(http.MethodGet, "/manifest", nil)
		w := httptest.NewRecorder()
		engine.HandleManifest(w, req)

		require.Equal(t, http.StatusInternalServerError, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Equal(t, "Failed to load VFS manifest", errPayload["error"])
	})
}

func TestHandleDownload(t *testing.T) {
	t.Parallel()

	t.Run("path shorter than prefix returns 400 without panic", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "dl-short-path")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodGet, "/download", nil)
		w := httptest.NewRecorder()
		engine.HandleDownload(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Equal(t, "Invalid blob hash", errPayload["error"])
	})

	t.Run("invalid hash returns 400", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "dl-invalid-hash")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodGet, protocol.PathDownloadPrefix+"not-a-valid-cas-hash", nil)
		w := httptest.NewRecorder()
		engine.HandleDownload(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Equal(t, "Invalid blob hash", errPayload["error"])
	})

	t.Run("non-existent hash returns 404", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "dl-not-found")
		engine := testutil.NewStorageEngine(t, cfg)

		dummyHash := strings.Repeat("a", 64)
		req := httptest.NewRequest(http.MethodGet, protocol.PathDownloadPrefix+dummyHash, nil)
		w := httptest.NewRecorder()
		engine.HandleDownload(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		var errPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&errPayload))
		require.Equal(t, "Blob not found", errPayload["error"])
	})

	t.Run("valid hash returns 200 with octet stream", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "dl-success")
		engine := testutil.NewStorageEngine(t, cfg)

		content := []byte("downloadable content stream")
		require.NoError(t, engine.SaveLocalFile("stream.bin", bytes.NewReader(content)))
		meta, exists, err := engine.GetFileMetaE("stream.bin")
		require.NoError(t, err)
		require.True(t, exists)

		req := httptest.NewRequest(http.MethodGet, protocol.PathDownloadPrefix+meta.Hash, nil)
		w := httptest.NewRecorder()
		engine.HandleDownload(w, req)

		resp := w.Result()
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, content, body)
	})
}

func TestHandleNotification(t *testing.T) {
	t.Parallel()

	t.Run("invalid json returns 400", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "notif-invalid-json")
		engine := testutil.NewStorageEngine(t, cfg)

		req := httptest.NewRequest(http.MethodPost, "/notify", strings.NewReader("{invalid-json"))
		w := httptest.NewRecorder()
		engine.HandleNotification(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("deleted notification returns 200", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "notif-deleted")
		engine := testutil.NewStorageEngine(t, cfg)

		notif := protocol.PeerNotification{
			File: protocol.IndexEntry{
				Name:    "del-notify.txt",
				Deleted: true,
				Version: 1,
				Hash:    strings.Repeat("0", 64),
			},
			Source: "peer-origin",
		}
		data, err := json.Marshal(notif)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(data))
		w := httptest.NewRecorder()
		engine.HandleNotification(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var respPayload map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&respPayload))
		require.Equal(t, "Metadata updated", respPayload["message"])
	})

	t.Run("upsert notification without subscription returns 200", func(t *testing.T) {
		cfg := testutil.DefaultConfig(t, "notif-upsert")
		engine := testutil.NewStorageEngine(t, cfg)

		notif := protocol.PeerNotification{
			File: protocol.IndexEntry{
				Name:    "new-file.txt",
				Deleted: false,
				Version: 1,
				Size:    100,
				Hash:    fmt.Sprintf("%064x", 12345),
			},
			Source: "peer-origin",
		}
		data, err := json.Marshal(notif)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/notify", bytes.NewReader(data))
		w := httptest.NewRecorder()
		engine.HandleNotification(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})
}
