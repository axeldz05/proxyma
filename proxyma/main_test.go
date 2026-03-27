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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func NewServer(id, storagePath, secret string) *Server {
    s := &Server{
        ID:         id,
//        Client:     &http.Client{Timeout: 10 * time.Second},
        Peers:      make(map[string]string),
        storage: 	*storage.NewStorage(storagePath),
        index:      make(map[string]IndexEntry),
    }
    
    os.MkdirAll(storagePath, 0755)
    
    mux := http.NewServeMux()
    mux.HandleFunc("/upload", s.authMiddleware(s.handleUpload))
    mux.HandleFunc("/notify", s.authMiddleware(s.handleNotification))
    mux.HandleFunc("/download/", s.authMiddleware(s.handleDownload))
    mux.HandleFunc("/peers", s.authMiddleware(s.GetPeers))
    mux.HandleFunc("/manifest", s.authMiddleware(s.handleManifest))
    
    s.server = httptest.NewServer(mux)
    s.Address = s.server.URL
	s.Client = s.server.Client()
	s.Secret = secret
    
    return s
}

func AnAcceptedFileForUpload(t *testing.T, fileName string) (bytes.Buffer, *multipart.Writer, string){
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

func Test01FirstServerIsAlreadySynced(t *testing.T){
	sv := NewServer("1", t.TempDir(), "test-secret")
	defer sv.Close()
    require.NoError(t, sv.SyncStorage())
}

func Test02AServerCanConnectToAnother(t *testing.T){
	sv1 := NewServer("1", t.TempDir(), "test-secret")
	sv2 := NewServer("2", t.TempDir(), "test-secret")
	defer sv1.Close()
	defer sv2.Close()
	sv1.AddPeer("2", sv2.Address)
	sv2.AddPeer("1", sv1.Address)

	req := httptest.NewRequest("GET", "/peers", nil)
	w := httptest.NewRecorder()
	sv2.GetPeers(w, req)	
	resp := w.Result()
	buf := new(strings.Builder)
	_, err := io.Copy(buf,resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	gotPeersOfSv2 := strings.TrimSpace(buf.String())
	expectedPeersOfSv2 := fmt.Sprintf(`{"1":"%s"}`, sv1.Address)

	req = httptest.NewRequest("GET", "/peers", nil)
	w = httptest.NewRecorder()
	sv1.GetPeers(w, req)	
	resp = w.Result()
	buf = new(strings.Builder)
	_, err = io.Copy(buf,resp.Body)
	if err != nil {
		t.Errorf("Could not copy response from %s", resp.Body)
	}
	gotPeersOfSv1 := strings.TrimSpace(buf.String())
	expectedPeersOfSv1 := fmt.Sprintf(`{"2":"%s"}`, sv2.Address)
	require.Equal(t,expectedPeersOfSv2,gotPeersOfSv2)
	require.Equal(t,expectedPeersOfSv1,gotPeersOfSv1)
	require.NoError(t, sv1.SyncStorage())
	require.NoError(t, sv2.SyncStorage())
}

func Test03AllServersSyncsToLastUpdated(t *testing.T){
	updatedServer := NewServer("1", t.TempDir(), "test-secret")
	noUpdatedServer := NewServer("2", t.TempDir(), "test-secret")
	noUpdatedServer2 := NewServer("3", t.TempDir(), "test-secret")
	defer updatedServer.Close()
	defer noUpdatedServer.Close()
	defer noUpdatedServer2.Close()
	
	updatedServer.AddPeer("2", noUpdatedServer.Address)
	updatedServer.AddPeer("3", noUpdatedServer2.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Address)
	noUpdatedServer.AddPeer("3", noUpdatedServer2.Address)

	noUpdatedServer.AddPeer("1", updatedServer.Address)
	noUpdatedServer.AddPeer("2", noUpdatedServer.Address)
	fileName := "test03.txt"
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t, fileName)

	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))
    
    req, err := http.NewRequest("POST", updatedServer.Address+"/upload", &requestBody)
	require.NoError(t, err)
    req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", "test-secret")
	resp, err := updatedServer.Client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

    if resp.StatusCode != http.StatusCreated {
        t.Errorf("Expected status Created, got %v", resp.StatusCode)
    }
	_, exists := updatedServer.index[fileName]
    if !exists {
        t.Errorf("Blob hash '%s' was not registered in the metadata", expectedHash)
    }

	downloadURL := fmt.Sprintf("%s/download/%s", updatedServer.Address, expectedHash)
	req, err = http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)
	req.Header.Set("Proxyma-Secret", "test-secret")
	resp, err = updatedServer.Client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	buf := new(strings.Builder)
	_, err = io.Copy(buf,resp.Body)
	if err != nil {
		t.Errorf("Could not copy fileContent from %s", resp.Body)
	}
	uploadedContent := buf.String()
    
    if uploadedContent != fileContent {
        t.Errorf("Expected content %s, got %s", fileContent, string(uploadedContent))
    }
	require.Eventually(t, func() bool {
        noUpdatedServer.mutex.RLock()
		_, exists := noUpdatedServer.index[fileName]
        noUpdatedServer.mutex.RUnlock()
        return exists
    }, 2*time.Second, 100*time.Millisecond, "All servers should have been synced to last updated files")
}

func Test04UploadEndpointReturnsAndRegistersHash(t *testing.T) {
	sv := NewServer("1", t.TempDir(), "test-secret")
	defer sv.Close()

	fileName := "test04.txt"
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t, fileName)
    
	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	req, err := http.NewRequest("POST", sv.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", "test-secret")
	resp, err := sv.Client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	sv.mutex.RLock()
	fileMeta, exists := sv.index[fileName]
	sv.mutex.RUnlock()

	require.True(t, exists, "The file should be registered in s.files")
	require.NotEmpty(t, fileMeta.Hash, "The metadata should include the hash")
	require.Equal(t, expectedHash, fileMeta.Hash, "The metadata's hash should be the same as the file content's hash")
}

func Test05P2PNetworkEventualConsistency(t *testing.T) {
	clusterSize := 3
	servers := make([]*Server, clusterSize)
	for i := 0; i < clusterSize; i++ {
		servers[i] = NewServer(fmt.Sprintf("node-%d", i), t.TempDir(), "test-secret")
		defer servers[i].Close()
	}

	// Full connection between the peers
	for i, current := range servers {
		for j, peer := range servers {
			if i != j {
				current.AddPeer(peer.ID, peer.Address)
			}
		}
	}
	fileName := "test05.txt"
	requestBody, writer, expectedContent := AnAcceptedFileForUpload(t, fileName)
	req, err := http.NewRequest("POST", servers[0].Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", "test-secret")
	resp, err := servers[0].Client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	
	hasher := sha256.New()
	hasher.Write([]byte(expectedContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	require.Eventually(t, func() bool {
		for _, srv := range servers {
			srv.mutex.RLock()
			meta, exists := srv.index[fileName]
			srv.mutex.RUnlock()
			
			if !exists || meta.Hash != expectedHash {
				return false
			}
		}
		return true
	}, 3*time.Second, 100*time.Millisecond, "The cluster couldn't synchronize the file at a reasonable time.")
}

func Test06DownloadEndpointUsesHashInsteadOfName(t *testing.T) {
	sv := NewServer("1", t.TempDir(), "test-secret")
	defer sv.Close()
	fileName := "test06.txt"
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t, fileName)
	req, err := http.NewRequest("POST", sv.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", "test-secret")
	resp, err := sv.Client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	
	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	downloadURL := fmt.Sprintf("%s/download/%s", sv.Address, expectedHash)
	reqDL, err := http.NewRequest("GET", downloadURL, nil)
	require.NoError(t, err)
	reqDL.Header.Set("Proxyma-Secret", "test-secret")
	respDL, err := sv.Client.Do(reqDL)
	require.NoError(t, err)
	defer respDL.Body.Close()

	require.Equal(t, http.StatusOK, respDL.StatusCode, "Server should answer with OK 200 status when requesting Hash")
	buf := new(strings.Builder)
	_, err = io.Copy(buf, respDL.Body)
	require.NoError(t, err)
	require.Equal(t, fileContent, buf.String(), "Downloaded content should be the same as the uploaded content")
}

func Test07NetworkRequestRespectsTimeouts(t *testing.T) {
	// A "trap" node that takes 5 seconds to respond
	slowPeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5*time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer slowPeer.Close()

	sv := NewServer("1", t.TempDir(), "test-secret")
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
	
	sv.mutex.RLock()
	_, exists := sv.index[fakeFile.Name]
	sv.mutex.RUnlock()
	require.False(t, exists, "The file should not be registered if the download failed or timed out")
}

func Test08UnauthorizedAccessIsRejected(t *testing.T) {
	sv := NewServer("1", t.TempDir(), "mi-secreto-super-seguro")
	defer sv.Close()

	resp, err := http.Get(sv.Address + "/peers")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "Reject requests without the Proxyma-Secret header")
	
	req, err := http.NewRequest("GET", sv.Address+"/peers", nil)
	require.NoError(t, err)
	req.Header.Set("Proxyma-Secret", "secreto-falso-de-un-hacker")
	
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "You must reject requests with the wrong secret")
}

func Test09ManifestEndpointReturnsCurrentState(t *testing.T) {
	sv := NewServer("1", t.TempDir(), "test-secret")
	defer sv.Close()

	fakeHash := "hash-simulado-999"
	fakeFile := IndexEntry{
		Name: "dataset_v2.csv",
		Size: 1024,
		Hash: fakeHash,
	}
	
	sv.mutex.Lock()
	sv.index[fakeFile.Name] = fakeFile
	sv.mutex.Unlock()

	req, err := http.NewRequest("GET", sv.Address+"/manifest", nil)
	require.NoError(t, err)
	req.Header.Set("Proxyma-Secret", "test-secret")
	
	resp, err := sv.Client.Do(req)
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
	sv1 := NewServer("1", t.TempDir(), "test-secret")
	defer sv1.Close()

	fileName := "missingFile.txt"
	requestBody, writer, fileContent := AnAcceptedFileForUpload(t, fileName)
	req, err := http.NewRequest("POST", sv1.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Proxyma-Secret", "test-secret")
	
	resp, err := sv1.Client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	defer resp.Body.Close()

	hasher := sha256.New()
	hasher.Write([]byte(fileContent))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	sv2 := NewServer("2", t.TempDir(), "test-secret")
	defer sv2.Close()
	sv2.AddPeer("1", sv1.Address)

	sv2.mutex.RLock()
	_, existsBefore := sv2.index[fileName]
	sv2.mutex.RUnlock()
	require.False(t, existsBefore, "Node 2 shouldn't have any files")

	err = sv2.SyncStorage()
	require.NoError(t, err, "SyncStorage shouldn't fail")
	sv2.mutex.RLock()
	fileMeta, existsAfter := sv2.index[fileName]
	sv2.mutex.RUnlock()

	require.True(t, existsAfter, "Node 2 should have the file of node 1 after executing SyncStorage")
	require.Equal(t, expectedHash, fileMeta.Hash)
}

func Test11VirtualFileSystemTracksFileUpdates(t *testing.T) {
	sv := NewServer("1", t.TempDir(), "test-secret")
	defer sv.Close()
	fileName := "test11.txt"
	requestBody, writer, content := AnAcceptedFileForUpload(t, fileName)
	req1, err := http.NewRequest("POST", sv.Address+"/upload", &requestBody)
	require.NoError(t, err)
	req1.Header.Set("Content-Type", writer.FormDataContentType())
	req1.Header.Set("Proxyma-Secret", "test-secret")
	resp1, err := sv.Client.Do(req1)
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

	req2, err := http.NewRequest("POST", sv.Address+"/upload", &requestBody2)
	require.NoError(t, err)
	req2.Header.Set("Content-Type", writer2.FormDataContentType())
	req2.Header.Set("Proxyma-Secret", "test-secret")

	resp2, err := sv.Client.Do(req2)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	resp2.Body.Close()

	hasher2 := sha256.New()
	hasher2.Write([]byte(content2))
	hash2 := hex.EncodeToString(hasher2.Sum(nil))

	sv.mutex.RLock()
	meta, exists := sv.index[fileName]
	sv.mutex.RUnlock()

	require.True(t, exists, "The system must track the file by its logic name")
	require.Equal(t, hash2, meta.Hash, "Index should point to the Version 2 Hash")
	require.NotEqual(t, hash1, meta.Hash, "Hash should have changed")
	require.Equal(t, 2, meta.Version, "Version of the file should have been incremented to 2")
}
