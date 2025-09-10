package storage

import (
	"os"
	"path/filepath"
)

// with clients
//type Server struct{
//	clients []string
//	storages map[string][]Storage
//}

type Server struct{
	baseDir string
	storages map[string]Storage
}

func (sv *Server)AmountOfStorages()int{
	return len(sv.storages)
}

func (sv *Server)CreateStorageForClient(aClient string, aStorageName string)(*Storage, error){
	newStoragePath := filepath.Join(sv.baseDir, aStorageName)
	newStorage := &Storage{baseDir: newStoragePath}
	_, exists := sv.storages[aClient]
	if exists {
		return nil, ErrClientAlreadyHasAStorage
	}
	sv.storages[aClient] = *newStorage
	os.Mkdir(newStoragePath,0o755)
	return newStorage, nil
}

func (sv *Server)GetStorageOfName(aNameOfStorage string)(*Storage,error){
	for _,anStorage := range(sv.storages){
		if anStorage.Name() == aNameOfStorage {
			return &anStorage, nil
		}
	}
	return nil, ErrFileDoesNotExist
}

func (sv *Server)ExistsStorageOfName(aNameOfStorage string)bool  {
	_, err := sv.GetStorageOfName(aNameOfStorage)
	if err == ErrFileDoesNotExist {
		return false
	} else if err != nil {
		panic(err) // should never happen
	}
	return true
}

func (sv *Server)RenameStorageOfName(aNameOfStorage string, newNameForStorage string)error{
	aStorage, err := sv.GetStorageOfName(aNameOfStorage)	
	if err != nil {
		return err
	}	
	oldPath := filepath.Join(sv.baseDir, aStorage.baseDir)
	newPath := filepath.Join(sv.baseDir, newNameForStorage)
	os.Rename(oldPath, newPath)
	aStorage.baseDir = newNameForStorage
	return nil
}
