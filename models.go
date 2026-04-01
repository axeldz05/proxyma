package main

import (
	"net/http/httptest"
	"proxyma/storage"
	"sync"
)

type IndexEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Hash    string `json:"hash"`
	Version int    `json:"version"`
	Deleted bool   `json:"deleted"`
}

type PeerNotification struct {
	File   IndexEntry `json:"file"`
	Source string     `json:"source"`
}

type DownloadJob struct {
	File   IndexEntry
	Source string
}

type Server struct {
	config			NodeConfig
	peerClient  	PeerClient
	Peers   		map[string]string
	storage 		storage.Storage
	vfs 			*VFS
	downloadQueue 	chan DownloadJob
	server 			*httptest.Server
	subscriptions   *sync.Map
}

type NodeConfig struct {
	ID          string
	Address     string
	StoragePath string
	Secret      string
	Workers     int
}

