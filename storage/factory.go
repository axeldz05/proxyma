package storage

func NewFileManager(baseDir string) *FileManager {
	return &FileManager{baseDir: baseDir}
}
