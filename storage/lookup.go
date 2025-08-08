package storage

import (
	"path/filepath"
)

func (fm *FileManager) LookUp(lookup map[string]string, name string)(string, error){
	pathToFile := lookup[name]	
	if pathToFile == ""{
		return "", ErrFileDoesNotExist
	}
	return filepath.Join(fm.baseDir, pathToFile), nil
}

func (fm *FileManager) LookUpAbsoluteFilePath(fileName string) (string,error) {
	if !fm.LookUpExistsFileName(fileName){
		return "",ErrFileDoesNotExist
	}
	return fm.LookUp(fm.userLookupFiles, fileName)
}

func (fm *FileManager) LookUpExistsFileName(fileName string) bool {
	_, exists := fm.userLookupFiles[fileName] 
	return exists
}

func (fm *FileManager) SaveFileNameIntoLookUp(fileName string)error{
	_, exists := fm.userLookupFiles[fileName]	
	if exists {
		return ErrFileAlreadyExist
	}
	newName, err := RandomName(fm, fileName)
	if err != nil{
		return err
	}
	fm.userLookupFiles[fileName] = newName
	return nil
}

func (fm *FileManager) RenameLookupFileName(originalFileName string, newfileName string)error{
	realName, exists := fm.userLookupFiles[originalFileName]	
	if !exists {
		return ErrFileDoesNotExist
	}
	delete(fm.userLookupFiles, originalFileName)
	fm.userLookupFiles[newfileName] = realName
	return nil
}

func (fm *FileManager) RenameLookupFolderName(originalFolderName string, newFolderName string)error{
	realName, exists := fm.userLookupFolders[originalFolderName]	
	if !exists {
		return ErrFileDoesNotExist
	}
	delete(fm.userLookupFolders, originalFolderName)
	fm.userLookupFolders[newFolderName] = realName
	return nil
}

func (fm *FileManager) RemoveFileNameLookUp(filename string)error{
	_, exists := fm.userLookupFiles[filename]	
	if !exists {
		return ErrFileDoesNotExist
	}
	delete(fm.userLookupFiles, filename)
	return nil
}

func (fm *FileManager) RemoveFolderNameLookUp(folderName string)error{
	_, exists := fm.userLookupFolders[folderName]	
	// should delete lookups that has this folderName as key
	if !exists {
		return ErrFileDoesNotExist
	}
	// recursively delete all folderNames and FileNames
	pathToFolder := fm.userLookupFolders[folderName]
	for key, value := range fm.userLookupFiles {
		relPath, err := filepath.Rel(value, pathToFolder)
		if err != nil {return err}
		if relPath == ".."{
			delete(fm.userLookupFiles, key)
		}
	}
	for key, value := range fm.userLookupFolders{
		relPath, err := filepath.Rel(value, pathToFolder)
		if err != nil {return err}
		if relPath == ".."{
			err = fm.RemoveFolderNameLookUp(key)
			if err != nil {return err}
		}
	}
	delete(fm.userLookupFolders, folderName)
	return nil
}

func (fm *FileManager) LookUpAbsoluteFolderPath(folderName string) (string,error) {
	return fm.LookUp(fm.userLookupFolders, folderName)
}

func (fm *FileManager) LookUpExistsFolderName(folderName string) bool {
	_, exists := fm.userLookupFolders[folderName] 
	return exists
}

func (fm *FileManager) SaveFolderNameIntoLookUp(folderName string)error{
	_, exists := fm.userLookupFolders[folderName]	
	if exists {
		return ErrFileAlreadyExist
	}
	newName, err := RandomName(fm, folderName)
	if err != nil {
		return err
	}
	fm.userLookupFolders[folderName] = newName
	return nil
}
