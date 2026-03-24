package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type Storage struct {
	baseDir string
}

func Map[T, U any](slice []T, fn func(T) U) []U {
	result := make([]U, len(slice))
	for i, v := range slice {
		result[i] = fn(v)
	}
	return result
}

func (st *Storage) Name() string {
	return filepath.Base(st.baseDir)
}

func (st *Storage) UploadFile(filePath string, content io.Reader) (string, error) {
	err := AssertValidPath(filePath)
	if err != nil {
		return "", err
	}
	fullPath := filepath.Join(st.baseDir, filePath)
	file, err := os.Create(fullPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	mw := io.MultiWriter(file, hasher)
	_, err = io.Copy(mw, content)
	return hex.EncodeToString(hasher.Sum(nil)), err
}

func ReadFileFromClient(clientIP string, pathToRead string) (io.ReadCloser, error) {
	// For now this is mocked. It should do a HTTP GET request to the client.
	return os.Open(pathToRead)
}

func (st *Storage) UploadFolderWithFiles(pathToUpload string, clientIP string) error {
	err := AssertValidPath(pathToUpload)
	if err != nil {
		return err
	}
	rootFolderName := filepath.Base(pathToUpload)
	rootFolder := filepath.Join(st.baseDir, rootFolderName)
	if err := st.CreateFolder(rootFolderName); err != nil {
		return err
	}
	return filepath.WalkDir(pathToUpload, func(path string, d fs.DirEntry, err error) error {
		relPathToUpload, err := filepath.Rel(pathToUpload, path)
		if err != nil {
			return err
		}
		err = AssertValidPath(relPathToUpload)
		if err != nil {
			return err
		}
		if !d.IsDir() {
			fileContent, err := ReadFileFromClient(clientIP, path)
			if err != nil {
				return err
			}
			defer fileContent.Close()

			newPath := filepath.Join(rootFolder, relPathToUpload)
			outFile, err := os.Create(newPath)
			if err != nil {
				return err
			}
			defer outFile.Close()

			_, err = io.Copy(outFile, fileContent)
			return err
		}
		if d.IsDir() && d.Name() != pathToUpload {
			newPath := filepath.Join(rootFolder, relPathToUpload)
			return os.MkdirAll(newPath, 0o755)
		}
		return nil
	})
}

func (st *Storage) AmountOfFiles() (error, int) {
	result := 0
	err := VisitAndDo(st, func(string, fs.DirEntry) error { result++; return nil },
		IsNotADir)

	if err != nil {
		return err, 0
	}
	return nil, result
}

func (st *Storage) FileExists(filePath string) (bool, error) {
	err := AssertValidPath(filePath)
	if err != nil {
		return false, err
	}
	fullPath := filepath.Join(st.baseDir, filePath)
	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !info.IsDir(), nil
}

func (st *Storage) DownloadFile(fileName string, w io.Writer) error {
	err := AssertValidPath(fileName)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(st.baseDir, fileName)

	file, err := os.Open(fullPath)
	if os.IsNotExist(err) {
		return ErrFileDoesNotExist
	}
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return ErrFileDoesNotExist
	}

	_, err = io.Copy(w, file)
	return err
}

func (st *Storage) RenameFile(fileName string, newFileName string) error {
	err := AssertValidPath(fileName)
	if err != nil {
		return err
	}
	err = AssertValidPath(newFileName)
	if err != nil {
		return err
	}

	fullPath := filepath.Join(st.baseDir, fileName)
	dir := filepath.Dir(fullPath)
	newFullPath := filepath.Join(dir, newFileName)

	info, err := os.Stat(fullPath)
	if os.IsNotExist(err) {
		return ErrFileDoesNotExist
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return ErrFileDoesNotExist
	}

	return os.Rename(fullPath, newFullPath)
}

func (st *Storage) CreateFolder(folderPath string) error {
	err := AssertValidPath(folderPath)
	if err != nil {
		return err
	}
	path := filepath.Join(st.baseDir, folderPath)
	exists, err := st.FolderExists(folderPath)
	if err != nil {
		return err
	}
	if exists {
		return ErrFileAlreadyExist
	}
	return os.Mkdir(path, 0o755)
}

func (st *Storage) FolderExists(folderPath string) (bool, error) {
	err := AssertValidPath(folderPath)
	if err != nil {
		return false, err
	}
	path := filepath.Join(st.baseDir, folderPath)
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (st *Storage) DeleteFile(pathToFile string) error {
	err := AssertValidPath(pathToFile)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(st.baseDir, pathToFile)
	return os.Remove(fullPath)
}

func (st *Storage) DeleteFolder(pathToFolder string) error {
	err := AssertValidPath(pathToFolder)
	if err != nil {
		return err
	}
	path := filepath.Join(st.baseDir, pathToFolder)
	return os.RemoveAll(path)
}
