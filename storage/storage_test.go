package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"crypto/sha256"
	"encoding/hex"

	"github.com/stretchr/testify/require"
)

func NewFileName() string {
	return "NewName:o"
}

func aFileAcceptedByStorage() (string, []byte) {
	return "hello.exe", []byte{1, 2, 3}
}

func aFileNotAcceptedByStorage() (string, []byte) {
	return "../hello.exe", []byte{1, 2, 3}
}

func aFileAcceptedByStorage2() (string, []byte) {
	return "goodbye.exe", []byte{4, 5, 6}
}

func aFolderAcceptedByStorage() string {
	return "IamAFolder"
}

func aSubFileAcceptedByStorage() (string, []byte){
	return "IamASubFile",  []byte("content 1")
}

func aSubFileAcceptedByStorage2() (string, []byte){
	return "IamASubFile2",  []byte("content 2")
}

func aSubFolderAcceptedByStorage() string {
	return "IamASubFolder"
}

func aSubSubFileAcceptedByStorage() (string, []byte){
	return "IamASubSubFile", []byte {1, 2, 3}
}

func aSubSubFileAcceptedByStorage2() (string, []byte){
	return "IamASubSubFile2", []byte {2, 9}
}

func aFolderWithFilesAcceptedByStorage(t *testing.T) string {
	tempDir := t.TempDir()

	root := filepath.Join(tempDir, "IamASuperFolder")
	err := os.MkdirAll(root, 0755)
	if err != nil {
		t.Fatal(err)
	}

	files := make(map[string][]byte)
	fileName, content := aSubFileAcceptedByStorage()
	files[fileName] = content
	fileName, content = aSubFileAcceptedByStorage2()
	files[fileName] = content
	for filename, content := range files {
		fullPath := filepath.Join(root, filename)
		err := os.WriteFile(fullPath, content, 0644)
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func aFolderWithSubFoldersAndFilesAcceptedByStorage(t *testing.T) string {
	tempDir := t.TempDir()

	root := filepath.Join(tempDir, "IamASuperFolder")
	err := os.MkdirAll(root, 0755)
	if err != nil {
		t.Fatal(err)
	}
	subFolderName := aSubFolderAcceptedByStorage()
	subFolder := filepath.Join(root, subFolderName)
	err = os.MkdirAll(subFolder, 0755)
	if err != nil {
		t.Fatal(err)
	}

	files := make(map[string][]byte)
	fileName, content := aSubFileAcceptedByStorage()
	files[fileName] = content
	fileName, content = aSubFileAcceptedByStorage2()
	files[fileName] = content

	fileName, content = aSubSubFileAcceptedByStorage()
	files[filepath.Join(subFolderName, fileName)] = content
	fileName, content = aSubSubFileAcceptedByStorage2()
	files[filepath.Join(subFolderName, fileName)] = content

	for filename, content := range files {
		fullPath := filepath.Join(root, filename)
		err := os.WriteFile(fullPath, content, 0644)
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func Test01StorageStartsWithNofiles(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	err, got := aStorage.AmountOfFiles()
	require.NoError(t, err)	
	want := 0
	require.Equal(t, want, got)
}

func Test02UploadingFilesIncreasesTheAmountOfFiles(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	
	fileName1, content1 := aFileAcceptedByStorage()
	_, err := aStorage.UploadFile(fileName1, bytes.NewReader(content1))
	require.NoError(t, err)
	
	fileName2, content2 := aFileAcceptedByStorage2()
	_, err = aStorage.UploadFile(fileName2, bytes.NewReader(content2))
	require.NoError(t, err)
	
	err, got := aStorage.AmountOfFiles()
	require.NoError(t, err)	
	want := 2
	require.Equal(t, got, want)
}

func Test03StorageRecognizesTheSameUploadedFile(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	uploadFileAndVerify(t, aStorage, fileName, content)
}

func Test04CanNotDownloadAFileThatDoesNotExistsInTheStorage(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName, _ := aFileAcceptedByStorage()
	
	var buf bytes.Buffer
	got := aStorage.DownloadFile(fileName, &buf)
	want := ErrFileDoesNotExist
	require.ErrorIs(t, got, want)
}

func Test05CanChangeTheNameOfAnUploadedFile(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, aStorage, fileName, content)
	require.NoError(t, aStorage.RenameFile(fileName, NewFileName()))
	assertFileExists(t, aStorage, NewFileName())
}

func Test06AfterChangingAFileNameThePreviousShouldNotLongerExist(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, aStorage, fileName, content)
	require.NoError(t, aStorage.RenameFile(fileName, NewFileName()))
	assertFileDoesNotExists(t, aStorage, fileName)
}

func Test07AChangedFileNameShouldMantainItsContent(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, aStorage, fileName, content)

	var wantBuf bytes.Buffer
	downloadFileErr := aStorage.DownloadFile(fileName, &wantBuf)
	require.NoError(t, downloadFileErr)

	require.NoError(t, aStorage.RenameFile(fileName, NewFileName()))

	var gotBuf bytes.Buffer
	downloadFileErr = aStorage.DownloadFile(NewFileName(), &gotBuf)
	require.NoError(t, downloadFileErr)

	require.Equal(t, wantBuf.Bytes(), gotBuf.Bytes())
}

func Test08CanNotChangeAFileNameThatDoesNotExist(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName, _ := aFileAcceptedByStorage()
	got := aStorage.RenameFile(fileName, NewFileName())
	want := ErrFileDoesNotExist
	require.ErrorIs(t, got, want)
}

func Test09CanCreateFolders(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	err := aStorage.CreateFolder(aFolderAcceptedByStorage())
	require.NoError(t, err)
	assertFolderExists(t, aStorage, aFolderAcceptedByStorage())
}

func Test10CanNotCreateAFolderWithSameNameAsAnotherInSameDir(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	err := aStorage.CreateFolder(aFolderAcceptedByStorage())
	require.NoError(t, err)
	err = aStorage.CreateFolder(aFolderAcceptedByStorage())
	require.ErrorIs(t, err, ErrFileAlreadyExist)
}

func Test11CanCreateAFolderInSubFolder(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	err := aStorage.CreateFolder(aFolderAcceptedByStorage())
	require.NoError(t, err)
	folderPath := filepath.Join(aFolderAcceptedByStorage(), aFolderAcceptedByStorage())
	err = aStorage.CreateFolder(folderPath)
	require.NoError(t, err)
	assertFolderExists(t, aStorage, folderPath)
}

func Test12OverwriteFileWithSameNameWhenUploading(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, aStorage, fileName, content)
	_, err := aStorage.UploadFile(fileName, bytes.NewReader(content))
	var buf bytes.Buffer
	err = aStorage.DownloadFile(fileName, &buf)
	err, actualAmount := aStorage.AmountOfFiles()
	require.Equal(t, 1, actualAmount)
	require.Equal(t, buf.Bytes(), content)
	require.NoError(t, err)
}

func Test13CanUploadAFolderWithMultipleFiles(t *testing.T) {
	folderToUpload := aFolderWithFilesAcceptedByStorage(t)
    testUploadFolder(t, folderToUpload)
}

func Test14CanUploadAFolderWithSubFolders(t *testing.T) {
	folderToUpload := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
    testUploadFolder(t, folderToUpload)
}

func Test15DoesNotDeleteADifferentFileThanTheSpecified(t *testing.T){
	aStorage := NewStorage(t.TempDir())	
	fileToDelete, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, aStorage, fileToDelete, content)
	remainingFileInTheStorage, content2 := aFileAcceptedByStorage2()
	noErrorUploadFile(t, aStorage, remainingFileInTheStorage, content2)
	require.NoError(t, aStorage.DeleteFile(fileToDelete))
	assertFileDoesNotExists(t, aStorage, fileToDelete)
	assertFileExists(t, aStorage, remainingFileInTheStorage)
}

func Test16CanDeleteAFileThatIsInASubFolder(t *testing.T){
	aStorage := NewStorage(t.TempDir())	
	folderToUpload := aFolderWithFilesAcceptedByStorage(t)
	require.NoError(t, aStorage.UploadFolderWithFiles(folderToUpload, validClient()))
	fileName, _ := aSubFileAcceptedByStorage()
	pathToFile := filepath.Join(filepath.Base(folderToUpload), fileName)
	require.NoError(t, aStorage.DeleteFile(pathToFile))
	assertFileDoesNotExists(t, aStorage, pathToFile)
}

func Test17DeletingAFolderAlsoRemovesItsFiles(t *testing.T){
	aStorage := NewStorage(t.TempDir())	
	folderToUpload := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
	require.NoError(t, aStorage.UploadFolderWithFiles(folderToUpload, validClient()))
	pathToFolder := filepath.Join(filepath.Base(folderToUpload), aSubFolderAcceptedByStorage())
	require.NoError(t, aStorage.DeleteFolder(pathToFolder))
	assertFolderDoesNotExists(t, aStorage, pathToFolder)
}

func Test18CanNotUploadAFileOrFolderOutsideOfRoot(t *testing.T){
	aStorage := NewStorage(t.TempDir())	
	fileName, content := aFileNotAcceptedByStorage()
	_, err := aStorage.UploadFile(fileName, bytes.NewReader(content))
	require.ErrorIs(t, err, ErrFileNameShouldNotTryToAccessParentFolder)
}

func Test19UploadFileReturnsSHA256Hash(t *testing.T) {
	aStorage := NewStorage(t.TempDir())
	fileName := "crypto_test.txt"
	content := "Super secret message!"
	hasher := sha256.New()
	hasher.Write([]byte(content))
	expectedHash := hex.EncodeToString(hasher.Sum(nil))

	gotHash, err := aStorage.UploadFile(fileName, bytes.NewReader([]byte(content)))
	
	require.NoError(t, err)
	require.Equal(t, expectedHash, gotHash, "Hash should be the exact SHA-256 of the file content")
}
