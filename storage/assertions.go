package storage
import(
	"regexp"
)
func AssertValidPath(filePath string) error {
	// checks if it starts with multiple dots
	var re = regexp.MustCompile(`^\W{1,}\.`)
	if re.MatchString(filePath) {
		return ErrFileNameShouldNotHaveMultipleDotsAtStart
	}
	return nil
}
