package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"proxyma/storage"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func DefaultConfigFor(t *testing.T, id string) NodeConfig {
	return NodeConfig{
		ID: id,
		StoragePath: t.TempDir(),
		Secret: "secret-key",
		Workers: 2,
	}
}

func CalculateHash(t *testing.T, content string) string {
	t.Helper()
	hasher := sha256.New()
	hasher.Write([]byte(content))
	return hex.EncodeToString(hasher.Sum(nil))
}

func UploadFileSimulated(t *testing.T, sv *Server, fileName, content string) string {
	t.Helper()
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)
	_, err = io.WriteString(fileWriter, content)
	require.NoError(t, err)
	writer.Close()

	reqUp, err := http.NewRequest("POST", sv.config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	reqUp.Header.Set("Content-Type", writer.FormDataContentType())
	reqUp.Header.Set("Proxyma-Secret", sv.config.Secret)
	
	respUp, err := sv.server.Client().Do(reqUp)
	require.NoError(t, err)
	defer respUp.Body.Close()
	
	require.Equal(t, http.StatusCreated, respUp.StatusCode, "The upload should have return status 201 Created")
	return CalculateHash(t, content)
}

func DeleteFileSimulated(t *testing.T, sv *Server, fileName string) {
	t.Helper()
	reqDel, err := http.NewRequest("DELETE", sv.config.Address+"/file?name="+fileName, nil)
	require.NoError(t, err)
	reqDel.Header.Set("Proxyma-Secret", sv.config.Secret)
	
	respDel, err := sv.server.Client().Do(reqDel)
	require.NoError(t, err)
	defer respDel.Body.Close()
	
	require.Equal(t, http.StatusOK, respDel.StatusCode, "Delete should have return 200 OK")
}

func NewServer(cfg NodeConfig) *Server {
	s := &Server{
		config: 	   cfg,
		Peers:         make(map[string]string),
		storage:       *storage.NewStorage(cfg.StoragePath),
		vfs:           NewVFS(),
		downloadQueue: make(chan DownloadJob, 1000),
		subscriptions: &sync.Map{},
	}

	os.MkdirAll(cfg.StoragePath, 0755)

	s.server = httptest.NewServer(s.MountHandlers())
	s.config.Address = s.server.URL
	for range s.config.Workers {
		go s.downloadWorker()
	}
	s.peerClient = NewHTTPPeerClient(s.server.Client(), s.config.Secret)

	return s
}

func AnAcceptedFileForUpload(t *testing.T, fileName string) (bytes.Buffer, *multipart.Writer, string) {
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	fileContent := "Hello!"
	_, err = io.WriteString(fileWriter, fileContent)
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	return requestBody, writer, fileContent
}

func Test01FirstServerIsAlreadySynced(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()
	require.NoError(t, sv.SyncStorage())
}

func Test02AServerCanConnectToAnother(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(DefaultConfigFor(t, "1"))
	sv2 := NewServer(DefaultConfigFor(t, "1"))
	defer sv1.Close()
	defer sv2.Close()
	sv1.AddPeer("2", sv2.config.Address)
	sv2.AddPeer("1", sv1.config.Address)

	req := httptest.NewRequest("GET", "/peers", nil)
	w := httptest.NewRecorder()
	sv2.GetPeers(w, req)
	resp := w.Result()
	buf := new(strings.Builder)
	_, err := io.Copy(buf, resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	gotPeersOfSv2 := strings.TrimSpace(buf.String())
	expectedPeersOfSv2 := fmt.Sprintf(`{"1":"%s"}`, sv1.config.Address)

	req = httptest.NewRequest("GET", "/peers", nil)
	w = httptest.NewRecorder()
	sv1.GetPeers(w, req)
	resp = w.Result()
	buf = new(strings.Builder)
	_, err = io.Copy(buf, resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	gotPeersOfSv1 := strings.TrimSpace(buf.String())
	expectedPeersOfSv1 := fmt.Sprintf(`{"2":"%s"}`, sv2.config.Address)
	require.Equal(t, expectedPeersOfSv2, gotPeersOfSv2)
	require.Equal(t, expectedPeersOfSv1, gotPeersOfSv1)
	require.NoError(t, sv1.SyncStorage())
	require.NoError(t, sv2.SyncStorage())
}

func assertRemoteHashToBeTheSameAs(t *testing.T, expectedHash string, fileContent string, updatedServer *Server) {
	t.Helper()
	downloadURL := fmt.Sprintf("%s/download/%s", updatedServer.config.Address, expectedHash)
	req, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)
	req.Header.Set("Proxyma-Secret", updatedServer.config.Secret)
	resp, err := updatedServer.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
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

func Test03AllServersSyncsToLastUpdated(t *testing.T) {
	t.Parallel()
	updatedServer := NewServer(DefaultConfigFor(t, "1"))
	noUpdatedServer := NewServer(DefaultConfigFor(t, "2"))
	noUpdatedServer2 := NewServer(DefaultConfigFor(t, "3"))
	defer updatedServer.Close()
	defer noUpdatedServer.Close()
	defer noUpdatedServer2.Close()

	updatedServer.AddPeer("2", noUpdatedServer.config.Address)
	updatedServer.AddPeer("3", noUpdatedServer2.config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.config.Address)
	noUpdatedServer.AddPeer("3", noUpdatedServer2.config.Address)

	noUpdatedServer.AddPeer("1", updatedServer.config.Address)
	noUpdatedServer.AddPeer("2", noUpdatedServer.config.Address)
	fileName := "test03.txt"
	noUpdatedServer.subscriptions.Store(fileName, true)
	noUpdatedServer2.subscriptions.Store(fileName, true)
	_, _, fileContent := AnAcceptedFileForUpload(t, fileName)
	
	expectedHash := UploadFileSimulated(t, updatedServer, fileName, fileContent)

	_, exists := updatedServer.vfs.Get(fileName)
	if !exists {
		t.Errorf("Blob hash '%s' was not registered in the metadata", expectedHash)
	}

	assertRemoteHashToBeTheSameAs(t, expectedHash, fileContent, updatedServer)
	require.Eventually(t, func() bool {
		_, exists := noUpdatedServer.vfs.Get(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond, "All servers should have been synced to last updated files")
}

func Test04UploadEndpointReturnsAndRegistersHash(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t, "1"))
	defer sv.Close()

	fileName := "test04.txt"
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t, fileName)

	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	req, err := http.NewRequest("POST", sv.config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", sv.config.Secret)
	resp, err := sv.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	fileMeta, exists := sv.vfs.Get(fileName)

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func Test05P2PNetworkEventualConsistency(t *testing.T) {
	t.Parallel()
	clusterSize := 3
	servers := make([]*Server, clusterSize)
	for i := 0; i < clusterSize; i++ {
		serverName := fmt.Sprintf("node-%d", i)
		servers[i] = NewServer(DefaultConfigFor(t, serverName))
		defer servers[i].Close()
	}

	// Full connection between the peers
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.config.ID, peer.config.Address)
			}
		}
	}
	fileName := "test05.txt"
	for _, srv := range servers {
		srv.subscriptions.Store(fileName, true)
	}
	requestBody, writer, expectedContent := AnAcceptedFileForUpload(t, fileName)
	req, err := http.NewRequest("POST", servers[0].config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", servers[0].config.Secret)
	resp, err := servers[0].server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	hasher := sha256.New()
	hasher.Write([]byte(expectedContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	require.Eventually(t, func() bool {
		for _, srv := range servers {

			meta, exists := srv.vfs.Get(fileName)

			if !exists || meta.Hash != expectedHash {
				return false
			}
		}
		return true
	}, 3*time.Second, 100*time.Millisecond, "The cluster couldn't synchronize the file at a reasonable time.")
}

func Test06DownloadEndpointUsesHashInsteadOfName(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t,"1"))
	defer sv.Close()
	fileName := "test06.txt"
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t, fileName)
	req, err := http.NewRequest("POST", sv.config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", sv.config.Secret)
	resp, err := sv.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	downloadURL := fmt.Sprintf("%s/download/%s", sv.config.Address, expectedHash)
	reqDL, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)
	reqDL.Header.Set("Proxyma-Secret", sv.config.Secret)
	respDL, err := sv.server.Client().Do(reqDL)
	require.NoError(t, err)
	defer respDL.Body.Close()

	require.Equal(t, http.StatusOK, respDL.StatusCode, "Server should answer with OK 200 status when requesting Hash")
	buf := new(strings.Builder)
	_, err = io.Copy(buf, respDL.Body)
	require.NoError(t, err)
	require.Equal(t, fileContent, buf.String(), "Downloaded content should be the same as the uploaded content")
}

func Test07NetworkRequestRespectsTimeouts(t *testing.T) {
	t.Parallel()
	// A "trap" node that takes 5 seconds to respond
	slowPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowPeer.Close()

	sv := NewServer(DefaultConfigFor(t,"1"))
	defer sv.Close()

	fakeFile := IndexEntry{
		Name: "trampa.txt",
		Size: 100,
		Hash: "hashfalso123",
	}

	start := time.Now()
	sv.downloadFileFromPeer(fakeFile, slowPeer.URL)
	duration := time.Since(start)
	require.Less(t, duration, 3*time.Second, "The request should have been aborted by Timeout before the slow node ended")

	_, exists := sv.vfs.Get(fakeFile.Name)

	require.False(t, exists, "The file should not be registered if the download failed or timed out")
}

func Test08UnauthorizedAccessIsRejected(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t,"1"))
	defer sv.Close()

	resp, err := http.Get(sv.config.Address + "/peers")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Reject requests without the Proxyma-Secret header")

	req, err := http.NewRequest("GET", sv.config.Address+"/peers", nil)
	require.NoError(t, err)
	req.Header.Set("Proxyma-Secret", "secreto-falso-de-un-hacker")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "You must reject requests with the wrong secret")
}

func Test09ManifestEndpointReturnsCurrentState(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t,"1"))
	defer sv.Close()

	fakeHash := "hash-simulado-999"
	fakeFile := IndexEntry{
		Name: "dataset_v2.csv",
		Size: 1024,
		Hash: fakeHash,
	}

	sv.vfs.Upsert(fakeFile)

	req, err := http.NewRequest("GET", sv.config.Address+"/manifest", nil)
	require.NoError(t, err)
	req.Header.Set("Proxyma-Secret", sv.config.Secret)

	resp, err := sv.server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "The endpoint /manifest must answer with status code: 200 OK")

	var manifest map[string]IndexEntry
	err = json.NewDecoder(resp.Body).Decode(&manifest)
	require.NoError(t, err, "The manifest must be a valid JSON in format: map[string]FileInfo")

	require.Contains(t, manifest[fakeFile.Name].Hash, fakeHash, "The manifest must contain the hash of the injected file")
	require.Equal(t, fakeFile.Name, manifest[fakeFile.Name].Name, "The filename must be the same as in the manifest")
}

func Test10SyncStorageDownloadsMissingFiles(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(DefaultConfigFor(t,"1"))
	defer sv1.Close()

	fileName := "missingFile.txt"
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t, fileName)
	req, err := http.NewRequest("POST", sv1.config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", sv1.config.Secret)

	resp, err := sv1.server.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	defer resp.Body.Close()

	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	sv2 := NewServer(DefaultConfigFor(t, "2"))
	defer sv2.Close()
	sv2.AddPeer("1", sv1.config.Address)
	sv2.subscriptions.Store(fileName, true)

	_, existsBefore := sv2.vfs.Get(fileName)

	require.False(t, existsBefore, "Node 2 shouldn't have any files")

	err = sv2.SyncStorage()
	require.NoError(t, err, "SyncStorage shouldn't fail")
	require.Eventually(t, func() bool {
		fileMeta, existsAfter := sv2.vfs.Get(fileName)
		if !existsAfter {
			return false
		}
		return fileMeta.Hash == expectedHash
	}, 2*time.Second, 100*time.Millisecond, "Node 2 should have the file of node 1 after executing SyncStorage")
}

func Test11VirtualFileSystemTracksFileUpdates(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t,"1"))
	defer sv.Close()
	fileName := "test11.txt"
	requestBody, writer, content := AnAcceptedFileForUpload(t, fileName)
	req1, err := http.NewRequest("POST", sv.config.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req1.Header.Set("Content-Type", writer.FormDataContentType())
	req1.Header.Set("Proxyma-Secret", sv.config.Secret)
	resp1, err := sv.server.Client().Do(req1)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp1.StatusCode)
	resp1.Body.Close()

	hasher1 := sha256.New()
	hasher1.Write([]byte(content))
	hash1 := hex.EncodeToString(hasher1.Sum(nil))

	var requestBody2 bytes.Buffer
	writer2 := multipart.NewWriter(&requestBody2)
	fileWriter2, err := writer2.CreateFormFile("file", fileName)
	require.NoError(t, err)
	content2 := "version 2!"
	io.WriteString(fileWriter2, content2)
	writer2.Close()

	req2, err := http.NewRequest("POST", sv.config.Address+"/upload", &requestBody2)
	require.NoError(t, err)
	req2.Header.Set("Content-Type", writer2.FormDataContentType())
	req2.Header.Set("Proxyma-Secret", sv.config.Secret)

	resp2, err := sv.server.Client().Do(req2)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	resp2.Body.Close()

	hasher2 := sha256.New()
	hasher2.Write([]byte(content2))
	hash2 := hex.EncodeToString(hasher2.Sum(nil))

	meta, exists := sv.vfs.Get(fileName)

	require.True(t, exists, "The system must track the file by its logic name")
	require.Equal(t, hash2, meta.Hash, "Index should point to the Version 2 Hash")
	require.NotEqual(t, hash1, meta.Hash, "Hash should have changed")
	require.Equal(t, 2, meta.Version, "Version of the file should have been incremented to 2")
}

func Test12WorkerPoolLimitsConcurrency(t *testing.T) {
	t.Parallel()
	mockFiles := make(map[string]string)
	var notifications []PeerNotification

	for i := range 5 {
		content := fmt.Sprintf("Contenido %d", i)
		hasher := sha256.New()
		hasher.Write([]byte(content))
		hash := hex.EncodeToString(hasher.Sum(nil))
		mockFiles[hash] = content
		fileName := fmt.Sprintf("archivo_%d.txt", i)
		notifications = append(notifications, PeerNotification{
			File: IndexEntry{Name: fileName, Hash: hash, Version: 1},
		})
	}

	slowPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		requestedHash := strings.TrimPrefix(r.URL.Path, "/download/")
		if content, exists := mockFiles[requestedHash]; exists {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(content))
		}
	}))
	defer slowPeer.Close()

	sv := NewServer(DefaultConfigFor(t,"1"))
	defer sv.Close()

	for i := range 5 {
		sv.subscriptions.Store(fmt.Sprintf("archivo_%d.txt", i), true)
	}

	start := time.Now()

	for _, notif := range notifications {
		notif.Source = slowPeer.URL
		body, _ := json.Marshal(notif)
		req, _ := http.NewRequest("POST", sv.config.Address+"/notify", bytes.NewReader(body))
		req.Header.Set("Proxyma-Secret", sv.config.Secret)
		resp, _ := sv.server.Client().Do(req)
		resp.Body.Close()
	}

	require.Eventually(t, func() bool {

		return len(sv.vfs.Snapshot()) == 5
	}, 5*time.Second, 100*time.Millisecond)

	duration := time.Since(start)

	require.GreaterOrEqual(t, duration, 2*time.Second, "Too fast. The Worker Pool isn't limiting the concurrency.")
	require.Less(t, duration, 4*time.Second, "Too slow. System is working sequentially.")
}

func Test13LocalDeleteCreatesTombstone(t *testing.T) {
	t.Parallel()
	sv := NewServer(DefaultConfigFor(t,"1"))
	defer sv.Close()

	fileName := "test13.txt"
	requestBody, writer, _ := AnAcceptedFileForUpload(t, fileName)
	reqUp, _ := http.NewRequest("POST", sv.config.Address+"/upload", &requestBody)
	reqUp.Header.Set("Content-Type", writer.FormDataContentType())
	reqUp.Header.Set("Proxyma-Secret", sv.config.Secret)
	respUp, _ := sv.server.Client().Do(reqUp)
	respUp.Body.Close()

	metaBefore, _ := sv.vfs.Get(fileName)

	require.False(t, metaBefore.Deleted, "File should have not been deleted previously")

	reqDel, _ := http.NewRequest("DELETE", sv.config.Address+"/file?name="+fileName, nil)
	reqDel.Header.Set("Proxyma-Secret", sv.config.Secret)
	respDel, err := sv.server.Client().Do(reqDel)
	require.NoError(t, err)
	defer respDel.Body.Close()
	require.Equal(t, http.StatusOK, respDel.StatusCode, "The endpoint DELETE should return 200 OK")

	metaAfter, exists := sv.vfs.Get(fileName)

	require.True(t, exists, "The IndexEntry of the file should still exist after deleting")
	require.True(t, metaAfter.Deleted, "Deleted should be true in the IndexEntry")
	require.Equal(t, metaBefore.Version+1, metaAfter.Version, "Version should have been incremented")

	// TODO: avoid using blobExists and make another function in the main instead
	existsInDisk, _ := sv.storage.BlobExists(metaBefore.Hash)
	require.False(t, existsInDisk, "The physical blob should have been deleted")
}

func Test14TombstonePropagatesToPeers(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(DefaultConfigFor(t, "1"))
	sv2 := NewServer(DefaultConfigFor(t, "2"))
	defer sv1.Close()
	defer sv2.Close()

	sv1.AddPeer("2", sv2.config.Address)
	sv2.AddPeer("1", sv1.config.Address)

	fileName := "test14.txt"
	sv1.subscriptions.Store(fileName, true)
	sv2.subscriptions.Store(fileName, true)
	requestBody, writer, _ := AnAcceptedFileForUpload(t, fileName)
	reqUp, _ := http.NewRequest("POST", sv1.config.Address+"/upload", &requestBody)
	reqUp.Header.Set("Content-Type", writer.FormDataContentType())
	reqUp.Header.Set("Proxyma-Secret", sv1.config.Secret)
	respUp, _ := sv1.server.Client().Do(reqUp)
	respUp.Body.Close()

	require.Eventually(t, func() bool {
		_, exists := sv2.vfs.Get(fileName)
		return exists
	}, 2*time.Second, 100*time.Millisecond)

	reqDel, _ := http.NewRequest("DELETE", sv1.config.Address+"/file?name="+fileName, nil)
	reqDel.Header.Set("Proxyma-Secret", sv1.config.Secret)
	respDel, _ := sv1.server.Client().Do(reqDel)
	respDel.Body.Close()

	require.Eventually(t, func() bool {
		meta, _ := sv2.vfs.Get(fileName)
		return meta.Deleted && meta.Version == 2
	}, 2*time.Second, 100*time.Millisecond, "Server2 should have processed the Tombstone")
}

func Test15SelectiveSynchronization(t *testing.T) {
	t.Parallel()
	sv1 := NewServer(DefaultConfigFor(t, "1"))
	sv2 := NewServer(DefaultConfigFor(t, "2"))
	defer sv1.Close()
	defer sv2.Close()

	fileAName := "fileA.txt"
	fileBName := "fileB.txt"

	requestBodyA, writerA, _ := AnAcceptedFileForUpload(t, fileAName)
	reqUpA, _ := http.NewRequest("POST", sv1.config.Address+"/upload", &requestBodyA)
	reqUpA.Header.Set("Content-Type", writerA.FormDataContentType())
	reqUpA.Header.Set("Proxyma-Secret", sv1.config.Secret)
	respUpA, _ := sv1.server.Client().Do(reqUpA)
	respUpA.Body.Close()

	requestBodyB, writerB, _ := AnAcceptedFileForUpload(t, fileBName)
	reqUpB, _ := http.NewRequest("POST", sv1.config.Address+"/upload", &requestBodyB)
	reqUpB.Header.Set("Content-Type", writerB.FormDataContentType())
	reqUpB.Header.Set("Proxyma-Secret", sv1.config.Secret)
	respUpB, _ := sv1.server.Client().Do(reqUpB)
	respUpB.Body.Close()

	// Sv2 subscribes ONLY to fileA via API
	reqSub, _ := http.NewRequest("POST", sv2.config.Address+"/subscribe?name="+fileAName, nil)
	reqSub.Header.Set("Proxyma-Secret", sv2.config.Secret)
	respSub, err := sv2.server.Client().Do(reqSub)
	require.NoError(t, err)
	defer respSub.Body.Close()
	require.Equal(t, http.StatusOK, respSub.StatusCode, "Subscribe API should return 200 OK")

	// Connect peers
	sv1.AddPeer("2", sv2.config.Address)
	sv2.AddPeer("1", sv1.config.Address)
	require.NoError(t, sv2.SyncStorage())

	// Verify sv2 gets file A
	require.Eventually(t, func() bool {
		_, exists := sv2.vfs.Get(fileAName)
		return exists
	}, 2*time.Second, 100*time.Millisecond, "Server2 should have synced the subscribed file A")

	// Verify sv2 DOES NOT get file B
	time.Sleep(1 * time.Second) // wait to ensure no late sync happens
	_, existsB := sv2.vfs.Get(fileBName)
	require.False(t, existsB, "Server2 should NOT sync file B because it is not subscribed")
}
