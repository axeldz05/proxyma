package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/server"
	"proxyma/internal/testutil"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type TestServer struct {
	*server.Server
	httpTestSrv *httptest.Server
}

func (ts *TestServer) Client() *http.Client {
	return ts.httpTestSrv.Client()
}

func NewServer(t *testing.T, cfg protocol.NodeConfig) *TestServer {
	caPath := filepath.Dir(cfg.StoragePath)
	err := p2p.InitCluster(caPath)
	require.NoError(t, err)
	err = p2p.IssueNodeCertificate(caPath, cfg.StoragePath, cfg.ID)
	require.NoError(t, err)
	caCertFile := filepath.Join(caPath, "ca.crt")
	nodeCertFile := filepath.Join(cfg.StoragePath, cfg.ID+".crt")
	nodeKeyFile := filepath.Join(cfg.StoragePath, cfg.ID+".key")
	serverTLS, clientTLS, err := p2p.LoadNodeTLS(caCertFile, nodeCertFile, nodeKeyFile)
	require.NoError(t, err)
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
	}

	app := server.New(cfg, httpClient)
	ts := httptest.NewUnstartedServer(app.MountHandlers())
	ts.TLS = serverTLS
	ts.StartTLS()

	ts.Client().Transport = httpClient.Transport
	app.SetAddress(ts.URL)

	t.Cleanup(func() {
        ts.Close()
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        err := app.Shutdown(ctx)
        require.NoError(t, err, "Node shutdown should not return an error")
    })

	return &TestServer{
		Server:      app,
		httpTestSrv: ts,
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
	defer func(){ _ = respUp.Body.Close() }()

	require.Equal(t, http.StatusCreated, respUp.StatusCode, "The upload should have return status 201 Created")
	return testutil.CalculateHash(t, content)
}

func DeleteFileSimulated(t *testing.T, sv *TestServer, fileName string) {
	t.Helper()
	reqDel, err := http.NewRequest("DELETE", sv.Config.Address+"/file?name="+fileName, nil)
	require.NoError(t, err)

	respDel, err := sv.Client().Do(reqDel)
	require.NoError(t, err)
	defer func(){ _ = respDel.Body.Close() }()

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

func assertRemoteHashToBeTheSameAs(t *testing.T, expectedHash string, fileContent string, updatedServer *TestServer) {
	t.Helper()
	downloadURL := fmt.Sprintf("%s/download/%s", updatedServer.Config.Address, expectedHash)
	req, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	resp, err := updatedServer.Client().Do(req)
	require.NoError(t, err)
	defer func(){ _ = resp.Body.Close() }()
	buf := new(strings.Builder)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		t.Errorf("Could not copy fileContent from %s", resp.Body)
	}
	uploadedContent := buf.String()

	if uploadedContent != fileContent {
		t.Errorf("Expected content %s, got %s", fileContent, string(uploadedContent))
	}
}

func TestFirstServerIsAlreadySynced(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"))
	require.NoError(t, sv.Storage.SyncStorage(sv.GetPeersCopy()))
}

func TestAServerCanConnectToAnother(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, testutil.DefaultConfig(t, "1"))
	sv2 := NewServer(t, testutil.DefaultConfig(t, "1"))
	sv1.AddPeer("2", sv2.Config.Address)
	sv2.AddPeer("1", sv1.Config.Address)

	gotPeersOfSv2 := strings.TrimSpace(GetPeersSimulated(t, sv2))
	expectedPeersOfSv2 := fmt.Sprintf(`{"1":"%s"}`, sv1.Config.Address)
	gotPeersOfSv1 := strings.TrimSpace(GetPeersSimulated(t, sv1))
	expectedPeersOfSv1 := fmt.Sprintf(`{"2":"%s"}`, sv2.Config.Address)

	require.Equal(t, expectedPeersOfSv2, gotPeersOfSv2)
	require.Equal(t, expectedPeersOfSv1, gotPeersOfSv1)
	require.NoError(t, sv1.Storage.SyncStorage(sv1.GetPeersCopy()))
	require.NoError(t, sv2.Storage.SyncStorage(sv2.GetPeersCopy()))
}

func TestP2PNetworkEventualConsistency(t *testing.T) {
	t.Parallel()
	clusterSize := 3
	servers := make([]*TestServer, clusterSize)
	for i := range clusterSize {
		serverName := fmt.Sprintf("%d", i)
		servers[i] = NewServer(t, testutil.DefaultConfig(t, serverName))
	}

	// Full connection between the peers
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.Config.ID, peer.Config.Address)
			}
		}
	}
	fileName := "test05.txt"
	for _, srv := range servers {
		srv.Storage.SetSubscription(fileName, true)
	}
	fileContent := "everyone wants me"
	expectedHash := UploadFileSimulated(t, servers[0], fileName, fileContent)
	require.Eventually(t, func() bool {
		for _, srv := range servers {

			meta, exists := srv.Storage.GetFileMeta(fileName)

			if !exists || meta.Hash != expectedHash {
				return false
			}
		}
		return true
	}, 3*time.Second, 100*time.Millisecond, "The cluster couldn't synchronize the file at a reasonable time.")
}

func TestAllServersSyncsToLastUpdated(t *testing.T) {
	t.Parallel()
	updatedServer := NewServer(t, testutil.DefaultConfig(t, "1"))
	noUpdatedServer := NewServer(t, testutil.DefaultConfig(t, "2"))
	noUpdatedServer2 := NewServer(t, testutil.DefaultConfig(t, "3"))

	updatedServer.AddPeer("2", noUpdatedServer.Config.Address)
	updatedServer.AddPeer("3", noUpdatedServer2.Config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Config.Address)
	noUpdatedServer.AddPeer("3", noUpdatedServer2.Config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Config.Address)
	noUpdatedServer.AddPeer("2", noUpdatedServer.Config.Address)
	fileName := "test03.txt"
	noUpdatedServer.Storage.SetSubscription(fileName, true)
	noUpdatedServer2.Storage.SetSubscription(fileName, true)

	fileContent := "Hello!!!"
	expectedHash := UploadFileSimulated(t, updatedServer, fileName, fileContent)

	_, exists := updatedServer.Storage.GetFileMeta(fileName)
	if !exists {
		t.Errorf("Blob hash '%s' was not registered in the metadata", expectedHash)
	}

	assertRemoteHashToBeTheSameAs(t, expectedHash, fileContent, updatedServer)
	require.Eventually(t, func() bool {
		_, exists := noUpdatedServer.Storage.GetFileMeta(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond, "All servers should have been synced to last updated files")
}

func TestUploadEndpointReturnsAndRegistersHash(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"))

	fileName := "test04.txt"
	fileContent := "testing"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	fileMeta, exists := sv.Storage.GetFileMeta(fileName)

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func TestDownloadEndpointUsesHash(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"))
	fileName := "test06.txt"
	fileContent := "Hello!!"
	expectedHash := UploadFileSimulated(t, sv, fileName, fileContent)

	downloadURL := fmt.Sprintf("%s/download/%s", sv.Config.Address, expectedHash)
	reqDL, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)

	respDL, err := sv.Client().Do(reqDL)
	require.NoError(t, err)
	defer func(){ _ = respDL.Body.Close() }()

	require.Equal(t, http.StatusOK, respDL.StatusCode, "Server should answer with OK 200 status when requesting Hash")
	buf := new(strings.Builder)
	_, err = io.Copy(buf, respDL.Body)
	require.NoError(t, err)
	require.Equal(t, fileContent, buf.String(), "Downloaded content should be the same as the uploaded content")
}

func TestManifestEndpointReturnsCurrentState(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"))

	fakeHash := "hash-simulado-999"
	fakeFile := protocol.IndexEntry{
		Name: "dataset_v2.csv",
		Size: 1024,
		Hash: fakeHash,
	}

	sv.Storage.Upsert(fakeFile)

	req, err := http.NewRequest("GET", sv.Config.Address+"/manifest", nil)
	require.NoError(t, err)

	resp, err := sv.Client().Do(req)
	require.NoError(t, err)
	defer func(){ _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode, "The endpoint /manifest must answer with status code: 200 OK")

	var manifest map[string]protocol.IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	require.NoError(t, err, "The manifest must be a valid JSON in format: map[string]FileInfo")

	require.Contains(t, manifest[fakeFile.Name].Hash, fakeHash, "The manifest must contain the hash of the injected file")
	require.Equal(t, fakeFile.Name, manifest[fakeFile.Name].Name, "The filename must be the same as in the manifest")
}

func TestTombstonePropagatesToPeers(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(t, testutil.DefaultConfig(t, "1"))
	sv2 := NewServer(t, testutil.DefaultConfig(t, "2"))

	sv1.AddPeer("2", sv2.Config.Address)
	sv2.AddPeer("1", sv1.Config.Address)

	fileName := "test14.txt"
	sv1.Storage.SetSubscription(fileName, true)
	sv2.Storage.SetSubscription(fileName, true)

	fileContent := "hello from test14!!"
	UploadFileSimulated(t, sv1, fileName, fileContent)

	require.Eventually(t, func() bool {
		_, exists := sv2.Storage.GetFileMeta(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond)

	DeleteFileSimulated(t, sv1, fileName)

	require.Eventually(t, func() bool {
		meta, _ := sv2.Storage.GetFileMeta(fileName)
		return meta.Deleted && meta.Version == 2
	}, 2*time.Second, 100*time.Millisecond, "Server2 should have processed the Tombstone")
}

func TestANodeReceivesSatisfactoryAnswerFromServiceRequest(t *testing.T) {
	t.Parallel()
	svWithService := NewServer(t, testutil.DefaultConfig(t, "1"))
	svDemandingService := NewServer(t, testutil.DefaultConfig(t, "2"))

	savedParameters := map[string]protocol.ServiceParameter{
		"image":    {Type: "string", Required: true},
		"language": {Type: "string", Required: false},
		"output":   {Type: "string", Required: false},
	}
	schema1 := protocol.ServiceSchema{
		Name:        "ocr",
		Description: "Standard Optical Character Recognition",
		Parameters:  savedParameters,
	}
	var mockHandler compute.ServiceHandler = func(context.Context, map[string]any) (map[string]any, error) {
        return map[string]any{}, nil
    }
	err := svWithService.Compute.RegisterNewService(schema1, mockHandler)
	require.NoError(t, err)

	svDemandingService.AddPeer(svWithService.Config.ID, svWithService.Config.Address)
	svWithService.AddPeer(svDemandingService.Config.ID, svDemandingService.Config.Address)

	query := protocol.DiscoveryQuery{
		Service:          "ocr",
		RequiredParams:   []string{"language"},
		SortStrategy:     protocol.StrategyFastest,
		PayloadSizeBytes: 1024 * 1024 * 5,
	}

	targetPeerAddr, serviceSchema, err := svDemandingService.RequestServiceToCluster(query)
	require.NoError(t, err)
	require.Equal(t, svWithService.Config.Address, targetPeerAddr, "Debería haber elegido al nodo 1")
	require.Equal(t, "Standard Optical Character Recognition", serviceSchema.Description)

	filledInputs := map[string]any{
		"image":    "fake-hash-12345",
		"language": "spa",
	}

	taskID := "job-999"
	reqPayload := protocol.TaskRequest{
		TaskID:  taskID,
		Service: "ocr",
		Payload: filledInputs,
		ReplyTo: svDemandingService.Config.Address + "/services/callback",
	}

	err = svDemandingService.DispatchTask(targetPeerAddr, reqPayload)
	require.NoError(t, err, "The node worker should have accepted the task")

	require.Eventually(t, func() bool {
		taskResult, exists := svDemandingService.Compute.GetTaskStatus(taskID)
		return exists && taskResult.Status == "completed"
	}, 2*time.Second, 100*time.Millisecond, "The completion Webhook never arrived")
}

func TestUnauthorizedAccessIsRejected(t *testing.T) {
	t.Parallel()
	sv := NewServer(t, testutil.DefaultConfig(t, "1"))

	clientWithoutCert := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := clientWithoutCert.Get(sv.Config.Address + "/peers")
	if resp != nil {
		defer func(){ _ = resp.Body.Close() }()
	}
	require.Error(t, err, "Should fail at TLS handshake due to missing client certs")
}
