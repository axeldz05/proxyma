package storage
import(
	"regexp"
)
func AssertValidPath(filePath string) error {
	// checks if it tries to access parent folder
	var re = regexp.MustCompile(`^\W{1,}\.(\\|\/)`)
	if re.MatchString(filePath) {
		return ErrFileNameShouldNotTryToAccessParentFolder
	}
	return nil
}
