package storage

import (
	"errors"
	"os"
	"fmt"
)
// make better error messages. Put context into it. FileDoesNotExist isnt enough
var (
	ErrFileDoesNotExist = os.ErrNotExist
	ErrFileAlreadyExist = os.ErrExist
	ErrFailedSanitizationOfFileName = errors.New("The sanitization failed")
	ErrFileNameShouldNotHaveMultipleDotsAtStart = errors.New("File name should not have multiple dots at the beginning of its name")
)

func SanitizeError(name, reason string) error {
	return fmt.Errorf("%w: name=%q, reason=%q", ErrFailedSanitizationOfFileName, name, reason)
}
