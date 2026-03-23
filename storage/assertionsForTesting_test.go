package storage

import(
	"bytes"
	"testing"
	"io/fs"
	"os"
	"path/filepath"
	"github.com/stretchr/testify/require"
)

func validClient()string{
	return "validClient"
}

func testUploadFolder(t *testing.T, folderToUpload string) {
    aStorage := NewStorage(t.TempDir())
    err := aStorage.UploadFolderWithFiles(folderToUpload, validClient())
    require.NoError(t, err)
    
    folderName := filepath.Base(folderToUpload)
    assertFolderExists(t, aStorage, folderName)
    assertUploadedFilesMatch(t, aStorage, folderToUpload, folderName)
}

func assertFileExists(t *testing.T, aStorage *Storage, fileName string) {
	exists, _ := aStorage.FileExists(fileName)
	require.True(t, exists)
}

func assertFileDoesNotExists(t *testing.T, aStorage *Storage, fileName string) {
	exists, _ := aStorage.FileExists(fileName)
	require.False(t, exists)
}

func noErrorUploadFile(t *testing.T, aStorage *Storage, fileName string, content []byte) {
	_, err := aStorage.UploadFile(fileName, bytes.NewReader(content))
	require.NoError(t, err)
}

func uploadFileAndVerify(t *testing.T, aStorage *Storage, fileName string, content []byte) {
	_, err := aStorage.UploadFile(fileName, bytes.NewReader(content))
	require.NoError(t, err)
	
	var got bytes.Buffer
	err = aStorage.DownloadFile(fileName, &got)
	require.NoError(t, err)
	require.Equal(t, content, got.Bytes())
}

func assertFolderExists(t *testing.T, aStorage *Storage, folderName string) {
    folderExists, err := aStorage.FolderExists(folderName)
    require.NoError(t, err)
    if !folderExists {
        t.Fatalf("The folder named '%v' should exist as direct subfolder to '%v'", folderName, aStorage.baseDir)
    }
}

func assertFolderDoesNotExists(t *testing.T, aStorage *Storage, folderPath string) {
    folderExists, err := aStorage.FolderExists(folderPath)
    require.NoError(t, err)
    if folderExists {
        t.Fatalf("The folder named '%v' should not exist as direct subfolder to '%v'", folderPath, aStorage.baseDir)
    }
}

func assertUploadedFilesMatch(t *testing.T, aStorage *Storage, folderToUpload, folderName string) {
    require.NoError(t, filepath.WalkDir(folderToUpload, func(path string, d fs.DirEntry, err error) error {
        relPath, err := filepath.Rel(folderToUpload, path)
        require.NoError(t, err)
        relPath = filepath.Join(folderName, relPath)
        
        if !d.IsDir() {
            fileContent, err := os.ReadFile(path)
            require.NoErrorf(t, err, "There is no file '%v' on root '%v'", path, folderToUpload)
            
			var gotContent bytes.Buffer
			err = aStorage.DownloadFile(relPath, &gotContent)
            require.NoErrorf(t, err, "There is no uploaded file '%v'. Root is '%v'", relPath, folderToUpload)
            require.Equal(t, fileContent, gotContent.Bytes())
        }
        
        if d.IsDir() && d.Name() != folderName {
            aStorage.FolderExists(relPath)
        }
        
        return nil
    }))
}
