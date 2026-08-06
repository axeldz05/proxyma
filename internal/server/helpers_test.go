package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mtlsClientForPeer(t *testing.T, sponsor *TestServer, peerID string) *http.Client {
	t.Helper()
	caPath := filepath.Dir(sponsor.Config.StoragePath)
	require.NoError(t, p2p.IssueNodeCertificate(caPath, sponsor.Config.StoragePath, peerID))
	caCertFile, _ := p2p.CACertPaths(caPath)
	nodeCertFile, nodeKeyFile := p2p.NodeCertPaths(sponsor.Config.StoragePath, peerID)
	_, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err)
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   clientTLS,
			DisableKeepAlives: true,
		},
	}
}

func UploadFileSimulated(t *testing.T, sv *TestServer, fileName, content string) string {
	t.Helper()
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)
	_, err = io.WriteString(fileWriter, content)
	require.NoError(t, err)
	err = writer.Close()
	require.NoError(t, err, "Failed to close multipart writer")

	reqUp, err := http.NewRequest("POST", sv.Config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	reqUp.Header.Set("Content-Type", writer.FormDataContentType())

	respUp, err := sv.Client().Do(reqUp)
	require.NoError(t, err)
	defer func() { _ = respUp.Body.Close() }()

	require.Equal(t, http.StatusCreated, respUp.StatusCode, "The upload should have return status 201 Created")
	return testutil.CalculateHash(t, content)
}

func assertRemoteHashToBeTheSameAs(t *testing.T, expectedHash string, fileContent string, targetServer *TestServer) {
	t.Helper()
	downloadURL := fmt.Sprintf("%s/download/%s", targetServer.Config.Address, expectedHash)
	req, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	resp, err := targetServer.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	buf := new(strings.Builder)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		t.Errorf("Could not copy fileContent from '%s'", resp.Body)
	}
	uploadedContent := buf.String()

	if uploadedContent != fileContent {
		t.Errorf("Expected content '%s', got '%s'", fileContent, string(uploadedContent))
	}
}

func DeleteFileSimulated(t *testing.T, sv *TestServer, fileName string) {
	t.Helper()
	reqDel, err := http.NewRequest("DELETE", sv.Config.Address+"/file?name="+fileName, nil)
	require.NoError(t, err)

	respDel, err := sv.Client().Do(reqDel)
	require.NoError(t, err)
	defer func() { _ = respDel.Body.Close() }()

	require.Equal(t, http.StatusOK, respDel.StatusCode, "Delete should have return 200 OK")
}

func GetPeersSimulated(t *testing.T, sv *TestServer) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/peers", nil)
	w := httptest.NewRecorder()
	sv.GetPeers(w, req)
	resp := w.Result()
	buf := new(strings.Builder)
	_, err := io.Copy(buf, resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	return buf.String()
}

func invalidMultipartWithoutFile(t *testing.T) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// write a non-file field to make a valid multipart, but no "file" part
	require.NoError(t, w.WriteField("dummy", "value"))
	require.NoError(t, w.Close())
	return &buf
}

func RequestManifestSimulated(t *testing.T, sv *TestServer) map[string]protocol.IndexEntry {
	t.Helper()
	req, err := http.NewRequest("GET", sv.Config.Address+"/manifest", nil)
	require.NoError(t, err)
	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var manifest map[string]protocol.IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	require.NoError(t, err)
	return manifest
}
