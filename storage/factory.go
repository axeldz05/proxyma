package storage

func NewFileManager(baseDir string) *FileManager {
	return &FileManager{baseDir: baseDir,
		userLookupFiles:   map[string]string{},
		userLookupFolders: map[string]string{}}
}
