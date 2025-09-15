package storage

func NewStorage(baseDir string) *Storage{
	return &Storage{
		baseDir: baseDir,
	}
}
