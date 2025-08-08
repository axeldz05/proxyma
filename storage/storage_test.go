package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
	fm := NewFileManager(t.TempDir())
	got := fm.AmountOfFiles()
	want := 0
	require.Equal(t, want, got)
}

func Test02UploadingFilesIncreasesTheAmountOfFiles(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	require.NoError(t, fm.UploadFile(aFileAcceptedByStorage()))
	require.NoError(t, fm.UploadFile(aFileAcceptedByStorage2()))
	got := fm.AmountOfFiles()
	want := 2
	require.Equal(t, got, want)
}

func Test03StorageRecognizesTheSameUploadedFile(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	uploadFileAndVerify(t, fm, fileName, content)
}

func Test04CanNotDownloadAFileThatDoesNotExistsInTheStorage(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	fileName, _ := aFileAcceptedByStorage()
	_, got := fm.DownloadFile(fileName)
	want := ErrFileDoesNotExist
	require.ErrorIs(t, got, want)
}

func Test05CanChangeTheNameOfAnUploadedFile(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, fm, fileName, content)
	require.NoError(t, fm.RenameFile(fileName, NewFileName()))
	assertFileExists(t, fm, NewFileName())
}
func Test06AfterChangingAFileNameThePreviousShouldNotLongerExist(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, fm, fileName, content)
	require.NoError(t, fm.RenameFile(fileName, NewFileName()))
	assertFileDoesNotExists(t, fm, fileName)
}

func Test07AChangedFileNameShouldMantainItsContent(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, fm, fileName, content)

	want, downloadFileErr := fm.DownloadFile(fileName)
	require.NoError(t, downloadFileErr)

	require.NoError(t, fm.RenameFile(fileName, NewFileName()))

	got, downloadFileErr := fm.DownloadFile(NewFileName())
	require.NoError(t, downloadFileErr)

	require.Equal(t, got, want)
}

func Test08CanNotChangeAFileNameThatDoesNotExist(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	fileName, _ := aFileAcceptedByStorage()
	got := fm.RenameFile(fileName, NewFileName())
	want := ErrFileDoesNotExist
	require.ErrorIs(t, got, want)
}

func Test09CanCreateFolders(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	err := fm.CreateFolder(aFolderAcceptedByStorage())
	require.NoError(t, err)
	assertFolderExists(t, fm, aFolderAcceptedByStorage())
}

func Test10CanNotCreateAFolderWithSameNameAsAnotherInSameDir(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	err := fm.CreateFolder(aFolderAcceptedByStorage())
	require.NoError(t, err)
	err = fm.CreateFolder(aFolderAcceptedByStorage())
	require.ErrorIs(t, err, ErrFileAlreadyExist)
}

func Test11CanCreateAFolderInSubFolder(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	err := fm.CreateFolder(aFolderAcceptedByStorage())
	require.NoError(t, err)
	folderPath := filepath.Join(aFolderAcceptedByStorage(), aFolderAcceptedByStorage())
	err = fm.CreateFolder(folderPath)
	require.NoError(t, err)
	assertFolderExists(t, fm, folderPath)
}

func Test12CanNotUploadAFileThatAlreadyExistsInSameFolder(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	fileName, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, fm, fileName, content)
	err := fm.UploadFile(fileName, content)
	require.ErrorIs(t, err, ErrFileAlreadyExist, fmt.Sprintf("got: '%v'", err))
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
	fm := NewFileManager(t.TempDir())	
	fileToDelete, content := aFileAcceptedByStorage()
	noErrorUploadFile(t, fm, fileToDelete, content)
	remainingFileInTheStorage, content := aFileAcceptedByStorage2()
	noErrorUploadFile(t, fm, remainingFileInTheStorage, content)
	require.NoError(t, fm.DeleteFile(fileToDelete))
	assertFileDoesNotExists(t, fm, fileToDelete)
	assertFileExists(t, fm, remainingFileInTheStorage)
}

func Test16CanDeleteAFileThatIsInASubFolder(t *testing.T){
	fm := NewFileManager(t.TempDir())	
	folderToUpload := aFolderWithFilesAcceptedByStorage(t)
	require.NoError(t, fm.UploadFolderWithFiles(folderToUpload, validIP()))
	fileName, _ := aSubFileAcceptedByStorage()
	pathToFile := filepath.Join(filepath.Base(folderToUpload), fileName)
	require.NoError(t, fm.DeleteFile(pathToFile))
	assertFileDoesNotExists(t, fm, pathToFile)
}

func Test17DeletingAFolderAlsoRemovesItsFiles(t *testing.T){
	fm := NewFileManager(t.TempDir())	
	folderToUpload := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
	require.NoError(t, fm.UploadFolderWithFiles(folderToUpload, validIP()))
	pathToFolder := filepath.Join(filepath.Base(folderToUpload), aSubFolderAcceptedByStorage())
	require.NoError(t, fm.DeleteFolder(pathToFolder))
	assertFolderDoesNotExists(t, fm, pathToFolder)
}

func Test18CanUpdateAFilesContent(t *testing.T){
	fm := NewFileManager(t.TempDir())	
	fileName, content := aFileAcceptedByStorage()
	newContent := []byte("hello!")
	require.NoError(t, fm.UploadFile(fileName, content))
	require.NoError(t, fm.UpdateFile(fileName, newContent))
	content, err := fm.DownloadFile(fileName)
	require.NoError(t, err)
	require.Equal(t, content, newContent)
}

func Test19CanNotUploadAFileOrFolderOutsideOfRoot(t *testing.T){
	fm := NewFileManager(t.TempDir())	
	fileName, content := aFileNotAcceptedByStorage()
	require.ErrorIs(t, fm.UploadFile(fileName, content), ErrFileNameShouldNotHaveMultipleDotsAtStart)
}

// TestxxFilesAreInTheirSubfolders

//func Test19ClientCanSynchronizeWithTheStorage(t *testing.T){
//	fm := NewFileManager(t.TempDir())	
//	folderToUpload := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
//	require.NoError(t, fm.UploadFolderWithFiles(folderToUpload, validIP()))
//	// el cliente deberia de armar un http hacia fm. Esto se logra mediante una interfaz
//	facade.SynchronizeClientWithServer(validIP(), fm)
//}
