package storage

import(
	"testing"
	"io/fs"
	"os"
	"path/filepath"
	"github.com/stretchr/testify/require"
)

func validIP()string{
	return "111.111.111"
}

func testUploadFolder(t *testing.T, folderToUpload string) {
    fm := NewFileManager(t.TempDir())
    err := fm.UploadFolderWithFiles(folderToUpload, validIP())
    require.NoError(t, err)
    
    folderName := filepath.Base(folderToUpload)
    assertFolderExists(t, fm, folderName)
    assertUploadedFilesMatch(t, fm, folderToUpload, folderName)
}

func assertFileExists(t *testing.T, fm *FileManager, fileName string) {
	exists:= fm.FileExists(fileName)
	require.True(t, exists)
}

func assertFileDoesNotExists(t *testing.T, fm *FileManager, fileName string) {
	exists := fm.FileExists(fileName)
	require.False(t, exists)
}

func noErrorUploadFile(t *testing.T, fm *FileManager, fileName string, content []byte) {
	require.NoError(t, fm.UploadFile(fileName, content))
}

func uploadFileAndVerify(t *testing.T, fm *FileManager, fileName string, content []byte) {
	require.NoError(t, fm.UploadFile(fileName, content))
	got, err := fm.DownloadFile(fileName)
	require.NoError(t, err)
	require.Equal(t, content, got)
}

func assertFolderExists(t *testing.T, fm *FileManager, folderName string) {
    folderExists, err := fm.FolderExists(folderName)
    require.NoError(t, err)
    if !folderExists {
        t.Fatalf("The folder named '%v' should exist as direct subfolder to '%v'", folderName, fm.baseDir)
    }
}

func assertFolderDoesNotExists(t *testing.T, fm *FileManager, folderPath string) {
    folderExists, err := fm.FolderExists(folderPath)
    require.NoError(t, err)
    if folderExists {
        t.Fatalf("The folder named '%v' should not exist as direct subfolder to '%v'", folderPath, fm.baseDir)
    }
}

func assertUploadedFilesMatch(t *testing.T, fm *FileManager, folderToUpload, folderName string) {
    require.NoError(t, filepath.WalkDir(folderToUpload, func(path string, d fs.DirEntry, err error) error {
        relPath, err := filepath.Rel(folderToUpload, path)
        require.NoError(t, err)
        relPath = filepath.Join(folderName, relPath)
        
        if !d.IsDir() {
            fileContent, err := os.ReadFile(path)
            require.NoErrorf(t, err, "There is no file '%v' on root '%v'", path, folderToUpload)
            gotContent, err := fm.DownloadFile(relPath)
            require.NoErrorf(t, err, "There is no uploaded file '%v'. Root is '%v'", relPath, folderToUpload)
            require.Equal(t, gotContent, fileContent)
        }
        
        if d.IsDir() && d.Name() != folderName {
            fm.FolderExists(relPath)
        }
        
        return nil
    }))
}
