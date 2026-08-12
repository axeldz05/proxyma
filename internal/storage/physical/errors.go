package storage

import (
	"errors"
	"os"
)

var (
	ErrFileDoesNotExist = os.ErrNotExist
	ErrBlobCorrupt      = errors.New("CAS blob is corrupt")
)
