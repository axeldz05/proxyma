package storage

import (
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

func (st *Storage) Name()string{
	return filepath.Base(st.baseDir)
}

func (st *Storage) UploadFile(filePath string, content []byte) error {
	err := AssertValidPath(filePath)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(st.baseDir, filePath)
	_, err = os.Stat(fullPath)
	switch {
	case err == nil:
		return ErrFileAlreadyExist
	case os.IsNotExist(err):
		return os.WriteFile(fullPath, content, 0644)
	default:
		return err
	}
}

func ReadFileFromClient(clientIP string, pathToRead string) ([]byte, error){
	return os.ReadFile(pathToRead)
}

func (st *Storage) UploadFolderWithFiles(pathToUpload string, clientIP string) error {
	err := AssertValidPath(pathToUpload)
	if err != nil {
		return err
	}
	rootFolderName := filepath.Base(pathToUpload)
	rootFolder := filepath.Join(st.baseDir, rootFolderName)
	if err := st.CreateFolder(rootFolderName); err != nil {return err}
	return filepath.WalkDir(pathToUpload, func(path string, d fs.DirEntry, err error) error {
		relPathToUpload, err := filepath.Rel(pathToUpload, path)
		if err != nil {
			return err
		}
		err = AssertValidPath(relPathToUpload)
		if err != nil {
			return err
		}
		if !d.IsDir(){
			// should probably ask the system that uploads the file to do the ReadFile itself.
			// should be receiving an IP where it asks for files by path
			fileContent, err := ReadFileFromClient(clientIP, path)
			if err != nil{
				return err
			}
			newPath := filepath.Join(rootFolder, relPathToUpload)
			return os.WriteFile(newPath, fileContent, 0644)
		}
		if d.IsDir() && d.Name() != pathToUpload{
			newPath := filepath.Join(rootFolder, relPathToUpload)
			return os.MkdirAll(newPath, 0o755)
		}
		return nil
	})
}

func (st *Storage) AmountOfFiles() (error, int) {
	result := 0
	err := VisitAndDo(st, func(string, fs.DirEntry) error { result++; return nil}, 
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
	return FindFileAndDo(st, filePath, func(path string, d fs.DirEntry) (bool, error) {
		return true, nil
	})
}

func (st *Storage) DownloadFile(fileName string) ([]byte, error) {
	fileExists, err := st.FileExists(fileName)
	if err != nil {
		return nil, err
	}
	if !fileExists {
		return nil, ErrFileDoesNotExist
	}

	return FindFileAndDo(st, fileName, func(path string, file fs.DirEntry) ([]byte, error) {
		return os.ReadFile(path)
	})
}

func (st *Storage) RenameFile(fileName string, newFileName string) error {
	found, err := FindFileAndDo(st, fileName, func(path string, file fs.DirEntry) (bool, error) {
		dir := filepath.Dir(path)
		newPath := filepath.Join(dir, newFileName)
		os.Rename(path, newPath)
		return true, nil
	})
	if !found {
		return ErrFileDoesNotExist
	}
	return err
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
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil { // else if
		return false, err
	}
	return true, nil
}

func (st *Storage) DeleteFile(pathToFile string) error {
	// struct{} takes 0 bytes
	_, err := FindFileAndDo(st, pathToFile, func(path string, de fs.DirEntry) (struct{}, error) {
		return struct{}{}, os.Remove(path)
	})
	return err
}

func (st *Storage) DeleteFolder(pathToFolder string) error {
	err := AssertValidPath(pathToFolder)
	if err != nil {
		return err
	}
	path := filepath.Join(st.baseDir, pathToFolder)
	return os.RemoveAll(path)
}

func (st *Storage) UpdateFile(pathToFile string, newContent []byte) error {
	err := AssertValidPath(pathToFile)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(st.baseDir, pathToFile)
	_, err = os.Stat(fullPath)
	if err != nil{
		return err
	}
	return os.WriteFile(fullPath, newContent, 0644)
}
