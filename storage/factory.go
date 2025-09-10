package storage

func NewServer(baseDir string) *Server {
	return &Server{
		baseDir: baseDir,
		storages: map[string]Storage{},
	}
}

func NewStorage(baseDir string) *Storage{
	return &Storage{
		baseDir: baseDir,
	}
}
