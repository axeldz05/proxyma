package storage

import (
	"errors"
	"os"
	"fmt"
)
// make better error messages. Put context into it. FileDoesNotExist isnt enough
var (
	ErrClientAlreadyHasAStorage = errors.New("client already has an storage")
	ErrFileDoesNotExist = os.ErrNotExist
	ErrFileAlreadyExist = os.ErrExist
	ErrFailedSanitizationOfFileName = errors.New("the sanitization failed")
	ErrFileNameShouldNotTryToAccessParentFolder = errors.New("file name should not have multiple dots with slashes at the beginning of its name")
)

func SanitizeError(name, reason string) error {
	return fmt.Errorf("%w: name=%q, reason=%q", ErrFailedSanitizationOfFileName, name, reason)
}
