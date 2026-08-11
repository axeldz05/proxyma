package storage

import (
	"net/http"
	"proxyma/internal/protocol"
	storage "proxyma/internal/storage/physical"
	"proxyma/internal/utils"
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
		hasBlob, _ := se.HasPhysicalBlob(notification.File.Hash)

		if !hasBlob {
			err := se.onDownloadNeeded(notification.File, notification.Source)
			if err != nil {
				utils.RespondError(w, http.StatusForbidden, "Network rejected the source")
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
	s.SetSubscription(fileName, true)
	s.logger.Info("Subscription added", "file", fileName)
	utils.RespondMessage(w, http.StatusOK, "Subscribed to "+fileName)
}

func (s *StorageEngine) HandleManifest(w http.ResponseWriter, r *http.Request) {
	utils.RespondJSON(w, http.StatusOK, s.vfs.Snapshot())
}

func (s *StorageEngine) HandleDelete(w http.ResponseWriter, r *http.Request) {
	fileName, ok := utils.GetRequiredQueryParam(w, r, "name")
	if !ok {
		return
	}

	err := s.DeleteLocalFile(fileName)
	if err != nil {
		utils.RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	utils.RespondMessage(w, http.StatusOK, "File deleted successfully")
}
