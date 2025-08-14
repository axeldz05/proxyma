package storage

import (
	"io/fs"
	"os"
	"path/filepath"
)

type FileManager struct {
	baseDir string
}

func Map[T, U any](slice []T, fn func(T) U) []U {
	result := make([]U, len(slice))
	for i, v := range slice {
		result[i] = fn(v)
	}
	return result
}

func (fm *FileManager) UploadFile(filePath string, content []byte) error {
	err := AssertValidPath(filePath)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(fm.baseDir, filePath)
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

func (fm *FileManager) UploadFolderWithFiles(pathToUpload string, clientIP string) error {
	err := AssertValidPath(pathToUpload)
	if err != nil {
		return err
	}
	rootFolderName := filepath.Base(pathToUpload)
	rootFolder := filepath.Join(fm.baseDir, rootFolderName)
	if err := fm.CreateFolder(rootFolderName); err != nil {return err}
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

func (fm *FileManager) AmountOfFiles() (error, int) {
	result := 0
	err := VisitAndDo(fm, func(string, fs.DirEntry) error { result++; return nil}, 
		IsNotADir)
	
	if err != nil {
		return err, 0
	}
	return nil, result
}

func (fm *FileManager) FileExists(filePath string) (bool, error) {
	err := AssertValidPath(filePath)
	if err != nil {
		return false, err
	}
	return FindFileAndDo(fm, filePath, func(path string, d fs.DirEntry) (bool, error) {
		return true, nil
	})
}

func (fm *FileManager) DownloadFile(fileName string) ([]byte, error) {
	fileExists, err := fm.FileExists(fileName)
	if err != nil {
		return nil, err
	}
	if !fileExists {
		return nil, ErrFileDoesNotExist
	}

	return FindFileAndDo(fm, fileName, func(path string, file fs.DirEntry) ([]byte, error) {
		return os.ReadFile(path)
	})
}

func (fm *FileManager) RenameFile(fileName string, newFileName string) error {
	found, err := FindFileAndDo(fm, fileName, func(path string, file fs.DirEntry) (bool, error) {
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

func (fm *FileManager) CreateFolder(folderPath string) error {
	err := AssertValidPath(folderPath)
	if err != nil {
		return err
	}
	path := filepath.Join(fm.baseDir, folderPath)
	exists, err := fm.FolderExists(folderPath)	
	if err != nil {
		return err
	}
	if exists {
		return ErrFileAlreadyExist
	}
	return os.Mkdir(path, 0o755)
}

func (fm *FileManager) FolderExists(folderPath string) (bool, error) {
	err := AssertValidPath(folderPath)
	if err != nil {
		return false, err
	}
	path := filepath.Join(fm.baseDir, folderPath)
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil { // else if
		return false, err
	}
	return true, nil
}

func (fm *FileManager) DeleteFile(pathToFile string) error {
	// struct{} takes 0 bytes
	_, err := FindFileAndDo(fm, pathToFile, func(path string, de fs.DirEntry) (struct{}, error) {
		return struct{}{}, os.Remove(path)
	})
	return err
}

func (fm *FileManager) DeleteFolder(pathToFolder string) error {
	err := AssertValidPath(pathToFolder)
	if err != nil {
		return err
	}
	path := filepath.Join(fm.baseDir, pathToFolder)
	return os.RemoveAll(path)
}

func (fm *FileManager) UpdateFile(pathToFile string, newContent []byte) error {
	err := AssertValidPath(pathToFile)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(fm.baseDir, pathToFile)
	_, err = os.Stat(fullPath)
	if err != nil{
		return err
	}
	return os.WriteFile(fullPath, newContent, 0644)
}
