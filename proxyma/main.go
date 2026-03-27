package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"proxyma/storage"
	"sync"
	"time"
)

type IndexEntry struct {
    Name string `json:"name"`
    Size int64  `json:"size"`
	Hash string `json:"hash"`
	Version int  `json:"version"`
    Deleted bool `json:"deleted"`
}

type PeerNotification struct {
    File   IndexEntry `json:"file"`
    Source string   `json:"source"`
}

type Server struct {
    ID      string
    Address string
    Client  *http.Client
    Peers   map[string]string
	Secret  string
    
	storage storage.Storage

	index map[string]IndexEntry
    mutex      sync.RWMutex
    
    server *httptest.Server
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


func (s *Server) SyncStorage() error {
	s.mutex.RLock()
	peers := make(map[string]string, len(s.Peers))
	for k, v := range s.Peers {
		peers[k] = v
	}
	s.mutex.RUnlock()
	for _, peerAddress := range peers{
		err := func(pAddr string) error {
			ctx, cancel := context.WithTimeout(context.Background(),5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx,"GET", peerAddress+"/manifest", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Proxyma-Secret", s.Secret)
			resp, err := s.Client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			var remoteManifest map[string]IndexEntry
			if err := json.NewDecoder(resp.Body).Decode(&remoteManifest); err != nil { 
				return err 
			}
			for logicalName, remoteFileInfo := range remoteManifest {
				s.mutex.RLock()
				localFileInfo, exists := s.index[logicalName]
				s.mutex.RUnlock()
				if !exists || remoteFileInfo.Version > localFileInfo.Version {
					s.downloadFileFromPeer(remoteFileInfo, peerAddress)
				}
			}
			return nil
		}(peerAddress)
		if err != nil {
			fmt.Printf("Warning: Failed to synchronize with peer %s: %v\n", peerAddress, err)
		}
	}
	return nil
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

	hash, err := s.storage.SaveBlob(file)
    if err != nil {
        http.Error(w, "Error saving blob: "+err.Error(), http.StatusInternalServerError)
        return
    }

	metaFileSize, metaFileName, err := getFileInfo(header)
	if err != nil {
		http.Error(w, "Error retrieving file metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}

    s.mutex.Lock()
    newVersion := 1
    if existingMeta, exists := s.index[metaFileName]; exists {
        newVersion = existingMeta.Version + 1
    }
    fileMeta := IndexEntry{
        Name:    metaFileName,
        Size:    metaFileSize,
        Hash:    hash,
        Version: newVersion,
    }
    s.index[metaFileName] = fileMeta
    s.mutex.Unlock()
    
    go s.notifyPeers(fileMeta)
    
    w.WriteHeader(http.StatusCreated)
    json.NewEncoder(w).Encode(map[string]string{
        "message": "Bloab uploaded successfully",
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
    err := s.storage.ReadBlob(requestedHash, w)
    if err != nil {
        if err == storage.ErrFileDoesNotExist {
            http.Error(w, "Blob not found", http.StatusNotFound)
        } else {
            http.Error(w, "Error retrieving blob: "+err.Error(), http.StatusInternalServerError)
        }
        return
    }
    w.Header().Set("Content-Type", "application/octet-stream")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
    health := map[string]interface{}{
        "status":   "healthy",
        "id":       s.ID,
        "files":    len(s.index),
        "peers":    len(s.Peers),
        "address":  s.Address,
    }
    
    json.NewEncoder(w).Encode(health)
}

func (s *Server) notifyPeers(fileInfo IndexEntry) {
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
        
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
        	fmt.Printf("Error making a request with context %s: %v\n", ctx, err)
			return
		}
		req.Header.Set("Proxyma-Secret", s.Secret)
		req.Header.Set("content-type", "application/json")
		resp, err := s.Client.Do(req)
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

func (s *Server) downloadFileFromPeer(fileInfo IndexEntry, sourceAddr string) {
    downloadURL := fmt.Sprintf("%s/download/%s", sourceAddr, fileInfo.Hash)
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
    if err != nil { 
        fmt.Printf("Error making a request with context %s: %v\n", ctx, err)
		return 
	}
    req.Header.Set("Proxyma-Secret", s.Secret)
    resp, err := s.Client.Do(req)
    if err != nil { 
        fmt.Printf("Error downloading blob %s: %v\n", fileInfo.Name, err)
		return 
	}
    defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK { 
        fmt.Printf("Error downloading blob%s: %s\n", fileInfo.Name, resp.Status)
		return 
	}
    savedHash, err := s.storage.SaveBlob(resp.Body)
    if err != nil { 
        fmt.Printf("Error saving blob %s: %v\n", fileInfo.Name, err)
		return 
	}
    if savedHash != fileInfo.Hash {
        fmt.Printf("SECURITY ALERT: Peer has sent corrupted or false hash. Expected hash: %s, got: %s\n", fileInfo.Hash, savedHash)
        return
    }

    s.mutex.Lock()
    s.index[fileInfo.Name] = fileInfo
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

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        clientSecret := r.Header.Get("Proxyma-Secret")
        if clientSecret != s.Secret {
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }
        next(w, r)
    }
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	s.mutex.RLock()
	json.NewEncoder(w).Encode(s.index)
	s.mutex.RUnlock()
}
