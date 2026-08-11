package storage

import (
	"net/http"
	"proxyma/internal/protocol"
	storage "proxyma/internal/storage/physical"
	"proxyma/internal/utils"
	"strings"
)

func (s *StorageEngine) HandleUpload(w http.ResponseWriter, r *http.Request) {

	err := r.ParseMultipartForm(10 << 20) // 10 MB limit
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Unable to parse form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		utils.RespondError(w, http.StatusBadRequest, "Error retrieving file")
		return
	}
	defer func() {
		if err = file.Close(); err != nil {
			s.logger.Error("failed to close file", "error", err)
		}
	}()

	err = s.SaveLocalFile(header.Filename, file)
	if err != nil {
		utils.RespondError(w, http.StatusInternalServerError, "Failed to save file")
		return
	}

	utils.RespondMessage(w, http.StatusCreated, "Blob uploaded successfully")
}

// handleNotification handles notifications from peers about new files
func (se *StorageEngine) HandleNotification(w http.ResponseWriter, r *http.Request) {
	notification, ok := utils.DecodeJSONOrError[protocol.PeerNotification](w, r)
	if !ok {
		return
	}
	if notification.File.Deleted {
		se.ProcessRemoteDeletion(notification.File)
		utils.RespondMessage(w, http.StatusOK, "Metadata updated")
		return
	}
	updated, err := se.upsertIndex(notification.File)
	if err != nil {
		utils.RespondError(w, http.StatusInternalServerError, "Failed to update metadata")
		return
	}
	if updated && se.IsSubscribed(notification.File.Name) {
		hasBlob, blobErr := se.HasPhysicalBlob(notification.File.Hash)
		if blobErr != nil {
			utils.RespondError(w, http.StatusInternalServerError, "Failed to check local blob")
			return
		}

		if !hasBlob {
			err := se.onDownloadNeeded(notification.File, notification.Source)
			if err != nil {
				if strings.Contains(err.Error(), "queue full") {
					utils.RespondError(w, http.StatusServiceUnavailable, "Download queue full")
					return
				}
				utils.RespondError(w, http.StatusBadGateway, "Could not enqueue download from source")
				return
			}
			utils.RespondMessage(w, http.StatusAccepted, "Downloading file")
			return
		}
	}
	utils.RespondMessage(w, http.StatusOK, "Metadata updated")
}

func (s *StorageEngine) HandleDownload(w http.ResponseWriter, r *http.Request) {
	requestedHash := r.URL.Path[len(protocol.PathDownloadPrefix):]
	if !storage.IsValidCASHash(requestedHash) {
		utils.RespondError(w, http.StatusBadRequest, "Invalid blob hash")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	err := s.physical.ReadBlob(requestedHash, w)
	if err != nil {
		if err == storage.ErrFileDoesNotExist {
			s.logger.Warn("Tried to download a blob that does not exist", "hash", requestedHash)
			utils.RespondError(w, http.StatusNotFound, "Blob not found")
			return
		}
		utils.RespondError(w, http.StatusInternalServerError, "Failed to download blob")
		return
	}
}

func (s *StorageEngine) HandleSubscribe(w http.ResponseWriter, r *http.Request) {
	fileName, ok := utils.GetRequiredQueryParam(w, r, "name")
	if !ok {
		return
	}
	if err := s.SetSubscription(fileName, true); err != nil {
		utils.RespondError(w, http.StatusInternalServerError, "Failed to persist subscription")
		return
	}
	s.logger.Info("Subscription added", "file", fileName)
	utils.RespondMessage(w, http.StatusOK, "Subscribed to "+fileName)
}

func (s *StorageEngine) HandleManifest(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.vfs.Snapshot()
	if err != nil {
		s.logger.Error("Failed to load VFS snapshot for manifest", "error", err)
		utils.RespondError(w, http.StatusInternalServerError, "Failed to load VFS manifest")
		return
	}
	utils.RespondJSON(w, http.StatusOK, snapshot)
}

func (s *StorageEngine) HandleDelete(w http.ResponseWriter, r *http.Request) {
	fileName, ok := utils.GetRequiredQueryParam(w, r, "name")
	if !ok {
		return
	}

	err := s.DeleteLocalFile(fileName)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			utils.RespondError(w, http.StatusNotFound, err.Error())
			return
		}
		utils.RespondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	utils.RespondMessage(w, http.StatusOK, "File deleted successfully")
}
