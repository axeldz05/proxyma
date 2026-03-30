package main

import (
	"context"
	"fmt"
	"io"
	"time"
)
func (s *Server) SyncStorage() error {
	peers := s.getPeersCopy()
	for _, peerAddress := range peers {
		err := func(pAddr string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			remoteManifest, err := s.peerClient.FetchManifest(ctx, peerAddress)
			if err != nil {
				return err
			}
			for logicalName, remoteFileInfo := range remoteManifest {
				localFileInfo, exists := s.vfs.Get(logicalName)
				if !exists || (exists && remoteFileInfo.Version > localFileInfo.Version) {
					s.downloadQueue <- DownloadJob{
						File:   remoteFileInfo,
						Source: peerAddress,
					}
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
	s.Peers[peerID] = address
}

func (s *Server) notifyPeers(fileInfo IndexEntry) {
	peers := make(map[string]string, len(s.Peers))
	for k, v := range s.Peers {
		peers[k] = v
	}

	for peerID, peerAddr := range peers {
		if peerID == s.config.ID {
			continue
		}
		payload := PeerNotification{
			File:   fileInfo,
			Source: s.config.Address,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.peerClient.Notify(ctx, peerAddr, payload)
		if err != nil {
			fmt.Printf("Error notifying peer %s: %v\n", peerID, err)
		}
	}
}

func (s *Server) downloadFileFromPeer(fileInfo IndexEntry, sourceAddr string) {
	if fileInfo.Deleted {
		savedFileInfo, exists := s.vfs.Get(fileInfo.Name)
		if s.vfs.Upsert(fileInfo) {
			if exists {
				s.storage.DeleteBlob(savedFileInfo.Hash)
			}
			fmt.Printf("file %s deleted.\n", fileInfo.Name)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body,err := s.peerClient.DownloadBlob(ctx, sourceAddr, fileInfo.Hash)
	if err != nil {
		fmt.Printf("Error downloading blob %s: %v\n", fileInfo.Name, err)
		return
	}
	defer body.Close()

	savedHash, _, err := s.storage.SaveBlob(body)
	if err != nil {
		fmt.Printf("Error saving blob %s: %v\n", fileInfo.Name, err)
		return
	}
	if savedHash != fileInfo.Hash {
		fmt.Printf("SECURITY ALERT: Peer has sent corrupted or false hash. Expected hash: %s, got: %s\n", fileInfo.Hash, savedHash)
		return
	}

	if s.vfs.Upsert(fileInfo) {
		fmt.Printf("Successfully downloaded and applied file %s\n", fileInfo.Name)
	} else {
		fmt.Printf("Download discarded: %s went obsolete while downloading\n", fileInfo.Name)
		s.storage.DeleteBlob(fileInfo.Hash)
	}
}

func (s *Server) DeleteLocalFile(fileName string) error {
	entry, exists := s.vfs.Get(fileName)
	if !exists {
		return fmt.Errorf("file not found: %s", fileName)
	}
	fileMeta := IndexEntry{
		Name:    entry.Name,
		Size:    entry.Size,
		Hash:    entry.Hash,
		Version: entry.Version + 1,
		Deleted: true,
	}
	if s.vfs.Upsert(fileMeta) {
		s.storage.DeleteBlob(entry.Hash)
		go s.notifyPeers(fileMeta)
	}
	return nil
}

func (s *Server) SaveLocalFile(fileName string, content io.Reader) error {
	hash, fileSize, err := s.storage.SaveBlob(content)
	if err != nil {
		return fmt.Errorf("Error saving blob: %s", err.Error())
	}

	newVersion := 1
	if existingMeta, exists := s.vfs.Get(fileName); exists {
		newVersion = existingMeta.Version + 1
	}
	fileMeta := IndexEntry{
		Name:    fileName,
		Size:    fileSize,
		Hash:    hash,
		Version: newVersion,
	}
	s.vfs.Upsert(fileMeta)

	go s.notifyPeers(fileMeta)

	return nil
}

func (s *Server) downloadWorker() {
	for job := range s.downloadQueue {
		s.downloadFileFromPeer(job.File, job.Source)
	}
}
