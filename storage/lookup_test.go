package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test01LookupStartsWithNoFiles(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	got := len(fm.userLookupFiles)
	want := 0
	require.Equal(t, got, want)
}

func Test02LookupStartsWithNoFolders(t *testing.T) {
	fm := NewFileManager(t.TempDir())
	got := len(fm.userLookupFolders)
	want := 0
	require.Equal(t, got, want)
}

func Test03LookupRecognizesTheAddedFile(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFileName, contentOfFile := aFileAcceptedByStorage()
	fm.UploadFile(aFileName, contentOfFile)
	require.True(t, fm.LookUpExistsFileName(aFileName))
	pathOfFile, err := fm.LookUpAbsoluteFilePath(aFileName)
	require.NoError(t, err)
	want, err := fm.DownloadFile(aFileName)
	require.NoError(t, err)
	got, err := os.ReadFile(pathOfFile)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, 1, len(fm.userLookupFiles))
}

func Test04LookupRecognizesTheAddedFolders(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFolderName := aFolderAcceptedByStorage()
	fm.CreateFolder(aFolderName)
	require.True(t, fm.LookUpExistsFolderName(aFolderName))
	folderExists, err := fm.FolderExists(aFolderName)
	require.NoError(t, err)
	require.True(t, folderExists)

	pathOfFolder, err := fm.LookUpAbsoluteFolderPath(aFolderName)
	require.NoError(t, err)
	got, err := os.Lstat(pathOfFolder)
	require.NoError(t, err)
	require.True(t, got.IsDir())
	require.Equal(t, 1, len(fm.userLookupFolders))
}

func Test05RemovingFilesAreReflectedInTheLookup(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFileName, contentOfFile := aFileAcceptedByStorage()
	err := fm.UploadFile(aFileName, contentOfFile)
	require.NoError(t, err)
	err = fm.DeleteFile(aFileName)
	require.NoError(t, err)
	require.Equal(t, 0, len(fm.userLookupFiles))
}

func Test06RemovingEmptyFoldersAreReflectedInTheLookup(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFolderName := aFolderAcceptedByStorage()
	err := fm.CreateFolder(aFolderName)
	require.NoError(t, err)
	fm.DeleteFolder(aFolderName)
	require.Equal(t, 0, len(fm.userLookupFolders))
}

func formatMap[T comparable, U any](aMap map[T]U) string{
	var b strings.Builder
	for k, v := range aMap {
		fmt.Fprintf(&b, "<%v, %v>\n", k, v)
	}
	dump := b.String()
	return dump
}

func Test07RemovingFoldersWithSubFoldersAreReflectedInTheLookup(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFolderPath := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
	err := fm.UploadFolderWithFiles(aFolderPath, validIP())
	require.NoError(t, err)
	err = fm.DeleteFolder(filepath.Base(aFolderPath))
	require.NoError(t, err)
	dump := formatMap(fm.userLookupFolders)
	require.Equalf(t, 0, len(fm.userLookupFolders), 
		"Number of folders is not equal to %v. Folders that should be deleted: \n%s",
		0,
		dump)
}

func Test08RemovingFoldersWithSubFilesAreReflectedInTheLookup(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFolderPath := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
	err := fm.UploadFolderWithFiles(aFolderPath, validIP())
	require.NoError(t, err)
	err = fm.DeleteFolder(filepath.Base(aFolderPath))
	require.NoError(t, err)
	dump := formatMap(fm.userLookupFiles)
	require.Equalf(t, 0, len(fm.userLookupFiles), 
		"Number of files is not equal to %v. Files that should be deleted: \n%s",
		0,
		dump)
}

func Test09RenamingAFileNameDoesNotChangeTheValue(t *testing.T){
	fm := NewFileManager(t.TempDir())
	oldName, content := aFileAcceptedByStorage()
	err := fm.UploadFile(oldName, content)
	require.NoError(t, err)
	newName := "hiii"
	err = fm.RenameFile(oldName, newName)
	require.NoError(t, err)
	require.True(t, fm.LookUpExistsFileName(newName))
	require.False(t, fm.LookUpExistsFileName(oldName))
	require.Equal(t, 1, len(fm.userLookupFiles))
}

func Test10RenamingAFolderNameDoesNotChangeTheValue(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFolderPath := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
	err := fm.UploadFolderWithFiles(aFolderPath, validIP())
	require.NoError(t, err)
	folderName := filepath.Base(aFolderPath)
	newName := "hiiii"

	pathBeforeRenaming, err := fm.LookUpAbsoluteFolderPath(folderName)
	require.NoError(t, err)

	err = fm.RenameFolder(folderName, newName)
	require.NoError(t, err)
	pathAfterRenaming, err := fm.LookUpAbsoluteFolderPath(newName)

	require.NoError(t, err)
	require.Equal(t, pathAfterRenaming, pathBeforeRenaming)
}

func Test11RenamingAFolderNameChangesAllItsReferencesInFolderLookup(t *testing.T){
	fm := NewFileManager(t.TempDir())
	aFolderPath := aFolderWithSubFoldersAndFilesAcceptedByStorage(t)
	err := fm.UploadFolderWithFiles(aFolderPath, validIP())
	require.NoError(t, err)

	folderNameToChange := filepath.Join(filepath.Base(aFolderPath), aSubFolderAcceptedByStorage())
	newFolderName := "hiiii"

	err = fm.RenameFolder(folderNameToChange, newFolderName)
	require.NoError(t, err)

	context := formatMap(fm.userLookupFiles)

	got := make([]string, 0, len(fm.userLookupFiles))
	for k := range fm.userLookupFiles {
		got = append(got, k)
	}
	expected := []string{"IamASuperFolder/IamASubFile", "IamASuperFolder/IamASubFile2", "IamASuperFolder/hiiii/IAmASubSubFile", "IamASuperFolder/hiiii/IAmASubSubFile2"}	
	require.Equalf(t, expected, got, context)
}
