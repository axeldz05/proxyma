package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"pcc/storage"
	"strings"
	"sync"
	"time"
)

// FileInfo represents metadata about a file
type FileInfo struct {
    Name string `json:"name"`
    Size int64  `json:"size"`
    URL  string `json:"url"` // URL to download the file from
}

// Server represents a node in our network
type Server struct {
    ID      string
    Address string
    Client  *http.Client
    Peers   map[string]string // Map of peer IDs to their addresses
    
    // File storage
	storage storage.Storage

	files map[string]FileInfo
    mutex      sync.RWMutex
    
    // HTTP server
    server *http.Server
}

func main(){
    // server := &http.Server{
    //     Addr: ":8080",
    // }

    // http.HandleFunc("/", handler)

    // log.Println("Starting file server on port 8080")
    // server.ListenAndServe()
	ctx := context.TODO() // use context.TODO() if you don't know what context to use.
	var body io.Reader = strings.NewReader("Hello, world!!") // nil readers are OK; it means there's no body.
	const method = "POST"
	const url = "https://eblog.fly.dev/index.html"
	req, err := http.NewRequestWithContext(ctx, method, url, body) // the function will parse the URL and set the Host header; invalid URLs will return an error.
	if err != nil {
		panic(err)
	}
	req.Header.Add("Accept-Encoding", "gzip")
    req.Header.Add("Accept-Encoding", "deflate")
    req.Header.Set("User-Agent", "eblog/1.0")
    req.Header.Set("some-key", "a value")   // will be canonicalized to Some-Key
    req.Header.Set("SOMe-KEY", "somevalue") // will overwrite the above since we used Set rather than Add
    req.Write(os.Stdout)
	log.Println(req.Header)
	// analogous but with post
	// const method = "POST"
	// const url = "https://eblog.fly.dev/index.html"
	// var body io.Reader = strings.NewReader("hello, world")
	// req, err := http.NewRequestWithContext(ctx, method, url, body)

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

// func (c *PccClient) SyncStorage()(bool, error){
// 	return true, nil
// }
// func (c *PccClient) UploadFile(filePath string, content []byte)error{
// 	return c.Storage.UploadFile(filePath, content)
// }


// AddPeer adds another server to this server's peer list
func (s *Server) AddPeer(peerID, address string) {
    s.mutex.Lock()
    defer s.mutex.Unlock()
    s.Peers[peerID] = address
}

// handleUpload handles file uploads from clients using custom storage
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    
    // Parse multipart form
    err := r.ParseMultipartForm(10 << 20) // 10 MB limit
    if err != nil {
        http.Error(w, "Unable to parse form", http.StatusBadRequest)
        return
    }
    
    // Get the file from the form data
    file, header, err := r.FormFile("file")
    if err != nil {
        http.Error(w, "Error retrieving file", http.StatusBadRequest)
        return
    }
    defer file.Close()
    
    // Use custom storage to save the file
    fileInfo, err := s.storage.Save(header.Filename, file, header.Size)
    if err != nil {
        http.Error(w, "Error saving file: "+err.Error(), http.StatusInternalServerError)
        return
    }
    
    // Create file info for metadata tracking
    fileMeta := FileInfo{
        Name: fileInfo.Name,
        Size: fileInfo.Size,
        URL:  fmt.Sprintf("%s/download/%s", s.Address, header.Filename),
    }
    
    // Store file metadata
    s.mutex.Lock()
    s.files[header.Filename] = fileMeta
    s.mutex.Unlock()
    
    // Notify all peers about the new file
    go s.notifyPeers(fileMeta)
    
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(map[string]string{
        "message": "File uploaded successfully",
        "file_id": fileInfo.ID, // Assuming your storage returns an ID
    })
}

// handleNotification handles notifications from peers about new files
func (s *Server) handleNotification(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    
    var fileInfo FileInfo
    err := json.NewDecoder(r.Body).Decode(&fileInfo)
    if err != nil {
        http.Error(w, "Invalid JSON", http.StatusBadRequest)
        return
    }
    
    // Check if we already have this file
    s.mutex.RLock()
    _, exists := s.files[fileInfo.Name]
    s.mutex.RUnlock()
    
    if exists {
        w.WriteHeader(http.StatusOK)
        fmt.Fprint(w, "File already exists")
        return
    }
    
    // Download the file from the peer
    go s.downloadFileFromPeer(fileInfo)
    
    w.WriteHeader(http.StatusAccepted)
    fmt.Fprint(w, "Notification received, downloading file")
}

// handleDownload serves files to peers using custom storage
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
    filename := r.URL.Path[len("/download/"):]
    
    // Check if file exists in our metadata
    s.mutex.RLock()
    _, exists := s.files[filename]
    s.mutex.RUnlock()
    
    if !exists {
        http.Error(w, "File not found", http.StatusNotFound)
        return
    }
    
    // Use custom storage to retrieve the file
    reader, err := s.storage.Retrieve(filename)
    if err != nil {
        http.Error(w, "Error retrieving file: "+err.Error(), http.StatusInternalServerError)
        return
    }
    defer reader.Close()
    
    // Set appropriate headers
    w.Header().Set("Content-Disposition", "attachment; filename="+filename)
    w.Header().Set("Content-Type", "application/octet-stream")
    
    // Stream the file to the client
    _, err = io.Copy(w, reader)
    if err != nil {
        http.Error(w, "Error sending file", http.StatusInternalServerError)
        return
    }
}

// handlePeers returns information about this server's peers
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
    s.mutex.RLock()
    defer s.mutex.RUnlock()
    
    json.NewEncoder(w).Encode(s.Peers)
}

// handleHealth returns server health information
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
    health := map[string]interface{}{
        "status":   "healthy",
        "id":       s.ID,
        "files":    len(s.files),
        "peers":    len(s.Peers),
        "address":  s.Address,
        "storage":  s.storage.Health(), // Assuming your storage has a health method
    }
    
    json.NewEncoder(w).Encode(health)
}

// notifyPeers notifies all peers about a new file
func (s *Server) notifyPeers(fileInfo FileInfo) {
    s.mutex.RLock()
    peers := make(map[string]string, len(s.Peers))
    for k, v := range s.Peers {
        peers[k] = v
    }
    s.mutex.RUnlock()
    
    for peerID, peerAddr := range peers {
        if peerID == s.ID {
            continue // Skip self
        }
        
        // Prepare the request
        url := fmt.Sprintf("%s/notify", peerAddr)
        body, err := json.Marshal(fileInfo)
        if err != nil {
            fmt.Printf("Error marshaling JSON for peer %s: %v\n", peerID, err)
            continue
        }
        
        // Send notification
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

// downloadFileFromPeer downloads a file from a peer server
func (s *Server) downloadFileFromPeer(fileInfo FileInfo) {
    // Download the file
    resp, err := s.Client.Get(fileInfo.URL)
    if err != nil {
        fmt.Printf("Error downloading file %s: %v\n", fileInfo.Name, err)
        return
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        fmt.Printf("Error downloading file %s: %s\n", fileInfo.Name, resp.Status)
        return
    }
    
    // Use custom storage to save the file
    savedFileInfo, err := s.storage.Save(fileInfo.Name, resp.Body, fileInfo.Size)
    if err != nil {
        fmt.Printf("Error saving file %s: %v\n", fileInfo.Name, err)
        return
    }
    
    // Update file metadata
    fileMeta := FileInfo{
        Name: savedFileInfo.Name,
        Size: savedFileInfo.Size,
        URL:  fmt.Sprintf("%s/download/%s", s.Address, fileInfo.Name),
    }
    
    s.mutex.Lock()
    s.files[fileInfo.Name] = fileMeta
    s.mutex.Unlock()
    
    fmt.Printf("Successfully downloaded file %s from peer\n", fileInfo.Name)
}

// Close shuts down the server
func (s *Server) Close() {
    s.server.Close()
}
