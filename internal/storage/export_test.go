package storage

// QueueBlobGCForTesting exposes queueBlobGCLocked for black-box storage_test tests.
func (se *StorageEngine) QueueBlobGCForTesting(hash string) error {
	se.mutationMu.Lock()
	defer se.mutationMu.Unlock()
	return se.queueBlobGCLocked(hash)
}
