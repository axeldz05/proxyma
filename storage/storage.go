package storage

import (
	"io/fs"
	"os"
	"path/filepath"
)

type FileManager struct {
	baseDir           string
	userLookupFiles   map[string]string
	userLookupFolders map[string]string
}

func Map[T, U any](slice []T, fn func(T) U) []U {
	result := make([]U, len(slice))
	for i, v := range slice {
		result[i] = fn(v)
	}
	return result
}

func VisitAllFiles(fm *FileManager, fn func(fs.DirEntry)) {
	filepath.WalkDir(fm.baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			fn(d)
		}
		return nil
	})
}

func FindFileAndDo[T any](fm *FileManager, fileName string, fn func(string, fs.DirEntry) (T, error)) (T, error) {
	var result T
	pathToFile, err := fm.LookUpAbsoluteFilePath(fileName)
	if err != nil {
		return result, err
	}
	pathToFile, err = filepath.Rel(fm.baseDir, pathToFile)

	if err != nil {
		return result, err
	}
	err = filepath.WalkDir(fm.baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(fm.baseDir, path)
		if err != nil {
			return err
		}
		if !d.IsDir() && relPath == pathToFile {
			var errFunc error
			result, errFunc = fn(path, d)
			if errFunc != nil {
				return errFunc
			}
			return filepath.SkipAll // when file is found, end the visitor
		}
		return nil
	})
	return result, err
}

func (fm *FileManager) UploadFile(fileName string, content []byte) error {
	if fm.LookUpExistsFileName(fileName) {
		return ErrFileAlreadyExist
	}
	err := fm.SaveFileNameIntoLookUp(fileName)
	if err != nil {
		return err
	}
	fullPath, err := fm.LookUpAbsoluteFilePath(fileName)
	if err != nil {
		return err
	}
	_, err = os.Stat(fullPath)
	switch {
	case err == nil:
		return SanitizeError(fileName, "Despite fileName being unique, the creation of a new fileName didn't create a unique fileName")
	case os.IsNotExist(err):
		return os.WriteFile(fullPath, content, 0644)
	default:
		return err
	}
}

func ReadFileFromClient(clientIP string, pathToRead string) ([]byte, error) {
	return os.ReadFile(pathToRead)
}

func (fm *FileManager) WriteNewFile(pathToUpload string, content []byte, perm os.FileMode) error {
	err := fm.SaveFileNameIntoLookUp(pathToUpload)

	if err != nil{
		return err
	}
	newPath, err := fm.LookUpAbsoluteFilePath(pathToUpload)
	if err != nil{
		return err
	}
	return os.WriteFile(newPath, content, perm)
}
func (fm *FileManager) UploadFolderWithFiles(pathToUpload string, clientIP string) error {
	folderName := filepath.Base(pathToUpload)
	if err := fm.CreateFolder(folderName); err != nil {
		return err
	}
	return filepath.WalkDir(pathToUpload, func(path string, d fs.DirEntry, err error) error {
		relPathToUpload, err := filepath.Rel(pathToUpload, path)
		relPathToUpload = filepath.Join(folderName, relPathToUpload)
		if !d.IsDir() {
			// should probably ask the system that uploads the file to do the ReadFile itself.
			// should be receiving an IP where it asks for files by path
			fileContent, err := ReadFileFromClient(clientIP, path)
			if err != nil {
				return err
			}
			return fm.WriteNewFile(relPathToUpload, fileContent, 0644)
		}
		if d.IsDir() && d.Name() != folderName {
			return fm.CreateFolder(relPathToUpload)
		}
		return nil
	})
}

func (fm *FileManager) AmountOfFiles() int {
	result := 0
	VisitAllFiles(fm, func(fs.DirEntry) { result++ })
	return result
}

func (fm *FileManager) FileExists(fileName string) bool {
	return fm.LookUpExistsFileName(fileName)
}

func (fm *FileManager) DownloadFile(fileName string) ([]byte, error) {
	// No hace falta esto, deberia de dar error en FindFileAndDo
	//fileExists := fm.FileExists(fileName)
	//if !fileExists {
	//	return nil, ErrFileDoesNotExist
	//}

	return FindFileAndDo(fm, fileName, func(path string, file fs.DirEntry) ([]byte, error) {
		return os.ReadFile(path)
	})
}

func (fm *FileManager) RenameFile(fileName string, newFileName string) error {
	found, err := FindFileAndDo(fm, fileName, func(path string, file fs.DirEntry) (bool, error) {
		renameErr := fm.RenameLookupFileName(fileName, newFileName)
		return true, renameErr 
	})
	if !found {
		return ErrFileDoesNotExist
	}
	return err
}

func (fm *FileManager) RenameFolder(folderName string, newFolderName string) error {
	if fm.LookUpExistsFolderName(folderName){
		return fm.RenameLookupFolderName(folderName, newFolderName)
	}
	return ErrFileDoesNotExist
}

func (fm *FileManager) CreateFolder(folderName string) error {

	exists := fm.LookUpExistsFolderName(folderName)
	if exists{
		return ErrFileAlreadyExist
	}

	err := fm.SaveFolderNameIntoLookUp(folderName)
	if err != nil {
		return err
	}
	path, err := fm.LookUpAbsoluteFolderPath(folderName)
	if err != nil{
		return err
	}
	return os.Mkdir(path, 0o755)
}

func (fm *FileManager) FolderExists(folderName string) (bool, error) {
	_, err := fm.LookUpAbsoluteFolderPath(folderName)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil { // else if
		return false, err
	}
	return true, nil
}

func (fm *FileManager) DeleteFile(fileName string) error {
	// struct{} takes 0 bytes
	_, err := FindFileAndDo(fm, fileName, func(path string, de fs.DirEntry) (struct{}, error) {
		removeFileErr := fm.RemoveFileNameLookUp(fileName)
		if removeFileErr != nil {
			return struct{}{}, removeFileErr
		}
		return struct{}{}, os.Remove(path)
	})
	return err
}

func (fm *FileManager) DeleteFolder(folderName string) error {
	fullPath, err := fm.LookUpAbsoluteFolderPath(folderName)
	if err != nil {
		return err
	}
	err = fm.RemoveFolderNameLookUp(folderName)
	if err != nil {
		return err
	}
	return os.RemoveAll(fullPath)
}

func (fm *FileManager) UpdateFile(fileName string, newContent []byte) error {
	fullPath, err := fm.LookUpAbsoluteFilePath(fileName)
	if err != nil {
		return err
	}
	_, err = os.Stat(fullPath)
	if err != nil {
		return err
	}
	return os.WriteFile(fullPath, newContent, 0644)
}
