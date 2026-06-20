package storage

// a kind of file clerk
import (
	"io/fs"
	"path/filepath"
)

func VisitAndDo(fm *Storage, execute func(string, fs.DirEntry) error, whenConditionIsMet func(string, fs.DirEntry) bool) error {
	return filepath.WalkDir(fm.baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if whenConditionIsMet(path, d) {
			err = execute(path, d)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func IsNotADir(path string, de fs.DirEntry) bool {
	return !de.IsDir()
}
