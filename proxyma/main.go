package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"proxyma/storage"
	"strings"
	"sync"
)

type FileInfo struct {
    Name string `json:"name"`
    Size int64  `json:"size"`
	Hash string `json:"hash"`
}

type PeerNotification struct {
    File   FileInfo `json:"file"`
    Source string   `json:"source"`
}

type Server struct {
    ID      string
    Address string
    Client  *http.Client
    Peers   map[string]string
    
	storage storage.Storage

	files map[string]FileInfo
    mutex      sync.RWMutex
    
    server *httptest.Server
}

func main(){
	ctx := context.TODO()
	var body io.Reader = strings.NewReader("Hello, world!!")
	const method = "POST"
	const url = "https://eblog.fly.dev/index.html"
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		panic(err)
	}
    req.Write(os.Stdout)
	log.Println(req.Header)
}

func HealthCheckHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func GetUpdateHandler(w http.ResponseWriter, r *http.Request){
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Updated"))
	w.Write([]byte("file1.txt"))
}

func (s *Server) GetPeers(w http.ResponseWriter, r *http.Request) {
    s.mutex.RLock()
    defer s.mutex.RUnlock()
	//for v,k := range s.Peers{
	//fmt.Printf("v: %s. k: %s", v,k)
	//}
    json.NewEncoder(w).Encode(s.Peers)
}


func (s *Server) SyncStorage()bool{
	return true
}

func (s *Server) AddPeer(peerID, address string) {
    s.mutex.Lock()
    defer s.mutex.Unlock()
    s.Peers[peerID] = address
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    
    err := r.ParseMultipartForm(10 << 20) // 10 MB limit
    if err != nil {
        http.Error(w, "Unable to parse form", http.StatusBadRequest)
        return
    }
    
    file, header, err := r.FormFile("file")
    if err != nil {
        http.Error(w, "Error retrieving file", http.StatusBadRequest)
        return
    }
    defer file.Close()
    
	hash, err := s.storage.UploadFile(header.Filename, file)
    if err != nil {
        http.Error(w, "Error saving file: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
	metaFileSize, metaFileName, err := getFileInfo(header)

	if err != nil {
        http.Error(w, "Error retrieving file metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fileMeta := FileInfo{
        Name: metaFileName,
        Size: metaFileSize,
		Hash: hash,
    }
    
    s.mutex.Lock()
    s.files[hash] = fileMeta
    s.mutex.Unlock()
    
    go s.notifyPeers(fileMeta)
    
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(map[string]string{
        "message": "File uploaded successfully",
    })
}

// handleNotification handles notifications from peers about new files
func (s *Server) handleNotification(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
	var notification PeerNotification
    err := json.NewDecoder(r.Body).Decode(&notification)
    if err != nil {
        http.Error(w, "Invalid JSON", http.StatusBadRequest)
        return
    }
    
	go s.downloadFileFromPeer(notification.File, notification.Source)    

    w.WriteHeader(http.StatusAccepted)
    fmt.Fprint(w, "Notification received, downloading file")
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
    requestedHash := r.URL.Path[len("/download/"):]
    
    s.mutex.RLock()
    fileMeta, exists := s.files[requestedHash]
    s.mutex.RUnlock()
    
    if !exists {
        http.Error(w, "File not found", http.StatusNotFound)
        return
    }
    
    err := s.storage.DownloadFile(fileMeta.Name, w)
    if err != nil {
        http.Error(w, "Error retrieving file: "+err.Error(), http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Disposition", "attachment; requestedHash="+requestedHash)
    w.Header().Set("Content-Type", "application/octet-stream")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
    health := map[string]interface{}{
        "status":   "healthy",
        "id":       s.ID,
        "files":    len(s.files),
        "peers":    len(s.Peers),
        "address":  s.Address,
    }
    
    json.NewEncoder(w).Encode(health)
}

func (s *Server) notifyPeers(fileInfo FileInfo) {
    s.mutex.RLock()
    peers := make(map[string]string, len(s.Peers))
    for k, v := range s.Peers {
        peers[k] = v
    }
    s.mutex.RUnlock()
    
    for peerID, peerAddr := range peers {
        if peerID == s.ID {
            continue
        }
        
        url := fmt.Sprintf("%s/notify", peerAddr)
        payload := PeerNotification{
            File:   fileInfo,
            Source: s.Address,
        }
        body, err := json.Marshal(payload)
        if err != nil {
            fmt.Printf("Error marshaling JSON for peer %s: %v\n", peerID, err)
            continue
        }
        
        resp, err := s.Client.Post(url, "application/json", bytes.NewReader(body))
        if err != nil {
            fmt.Printf("Error notifying peer %s: %v\n", peerID, err)
            continue
        }
        resp.Body.Close()
        
        if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
            fmt.Printf("Peer %s returned status: %s\n", peerID, resp.Status)
        }
    }
}

// This should verify that it comes from a valid peer.
func (s *Server) downloadFileFromPeer(fileInfo FileInfo, sourceAddr string) {
    downloadURL := fmt.Sprintf("%s/download/%s", sourceAddr, fileInfo.Hash)
    resp, err := s.Client.Get(downloadURL)

    if err != nil {
        fmt.Printf("Error downloading file %s: %v\n", fileInfo.Name, err)
        return
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        fmt.Printf("Error downloading file %s: %s\n", fileInfo.Name, resp.Status)
        return
    }
	_, err = s.storage.UploadFile(fileInfo.Name, resp.Body)
    if err != nil {
        fmt.Printf("Error saving file %s: %v\n", fileInfo.Name, err)
        return
    }
    s.mutex.Lock()
    s.files[fileInfo.Hash] = fileInfo
    s.mutex.Unlock()
    
    fmt.Printf("Successfully downloaded file %s from peer\n", fileInfo.Name)
}

func (s *Server) Close() {
    s.server.Close()
}

func getFileInfo(header *multipart.FileHeader) (int64, string, error) {
    file, err := header.Open()
    if err != nil {
        return 0, "", err
    }
    defer file.Close()

    if stat, ok := file.(interface{ Stat() (os.FileInfo, error) }); ok {
        info, err := stat.Stat()
        if err != nil {
            return 0, "", err
        }
        return info.Size(), info.Name(), nil
    }

    size, err := file.Seek(0, io.SeekEnd)
    if err != nil {
        return 0, "", err
    }
    _, err = file.Seek(0, io.SeekStart)
    if err != nil {
        return 0, "", err
    }
    return size, header.Filename, nil
}
