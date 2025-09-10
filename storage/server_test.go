package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test1ClientCanNotHaveMoreThanOneStorageInTheServer(t *testing.T){
	aServer := NewServer(t.TempDir())
	aStorage, err := aServer.CreateStorageForClient(validClient(), "AStorage")
	require.NoError(t, err)
	aStorage2, err := aServer.CreateStorageForClient(validClient(), "ASecondStorage")
	require.ErrorIs(t, err, ErrClientAlreadyHasAStorage)
	want := 1
	got := aServer.AmountOfStorages()
	require.NotNil(t, aStorage)
	require.Nil(t, aStorage2)
	require.Equal(t, want,got)
	require.True(t, aServer.ExistsStorageOfName("AStorage"))
	require.False(t, aServer.ExistsStorageOfName("ASecondStorage"))
}

func Test2StorageIsRecognizedByTheServer(t *testing.T)  {
	aServer := NewServer(t.TempDir())
	aStorage, err := aServer.CreateStorageForClient(validClient(), "AStorage")
	require.NoError(t, err)
	serverRootPath := aServer.baseDir	
	storageRootPath := filepath.Join(serverRootPath, "AStorage")
	fileInfo, err := os.Lstat(storageRootPath)
	require.NoError(t, err)
	require.True(t, fileInfo.IsDir())
	require.Equal(t, fileInfo.Name(), aStorage.Name())
	require.Equal(t, storageRootPath, aStorage.baseDir)
}

// func Test2ServerCanRemoveAStorage(t *testing.T){
// 	aServer := NewServer(t.TempDir())
// 	aServer.CreateStorageForClient(validIP(), "AStorage")
// 	err := aServer.RemoveStorageOfName("AStorage")
// 	require.NoError(t, err)
// 	want := 0
// 	got := aServer.AmountOfStorages()
// 	require.Equal(t, want,got)
// 	require.False(t, aServer.ExistsStorageOfName("AStorage"))
// }
// 
// func Test3ServerCanRenameAStorage(t *testing.T){
// 	aServer := NewServer(t.TempDir())
// 	aServer.CreateStorageForClient(validIP(), "AStorage")
// 	err := aServer.RenameStorageOfName("AStorage", "RenamedStorage")
// 	require.NoError(t, err)
// 	want := 1
// 	got := aServer.AmountOfStorages()
// 	require.Equal(t, want,got)
// 	require.True(t, aServer.ExistsStorageOfName("RenamedStorage"))
// 	require.False(t, aServer.ExistsStorageOfName("AStorage"))
// }
// // Cannot create storage with an existing name
// func Test4AClientConnectedToTheServerStartsWithAStorage(t *testing.T){
// 	aServer := NewServer(t.TempDir())
// 	aServer.CreateStorageForClient(validIP(), "AStorage")
// 	want := 1
// 	got := aServer.AmountOfStorages()
// 	require.Equal(t, want,got)
// 	require.False(t, aServer.ExistsStorageOfName("AStorage"))
// 	clientsStorage := aServer.GetClientsStorage(validIP())
// }
