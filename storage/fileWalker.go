package storage
// a kind of file clerk
import(
	"path/filepath"
	"io/fs"
)
func VisitAndDo(fm *Storage, execute func(string, fs.DirEntry)error, whenConditionIsMet func(string, fs.DirEntry)bool) error {
	return filepath.WalkDir(fm.baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if whenConditionIsMet(path,d) {
			err = execute(path, d)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func ExistsFileRelativeToBase(baseDir string, pathToFile string)(func(path string, de fs.DirEntry)bool){
	return func(path string, de fs.DirEntry)bool {
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			// this should never happen
			panic(err)
		}
		return !de.IsDir() && relPath == pathToFile
	}
}

func FindFileAndDo[T any](fm *Storage, pathToFile string, fn func(string, fs.DirEntry) (T, error)) (T, error) {
	var result T

	onFileFoundDo := func(path string, d fs.DirEntry) error{
		res, err := fn(path, d)
		if err != nil {
			return err
		}
		result = res
		return filepath.SkipAll // when file is found, end the visitor
	}
	err := VisitAndDo(fm, 
		onFileFoundDo, ExistsFileRelativeToBase(fm.baseDir, pathToFile))
	return result, err
}

func IsNotADir(path string, de fs.DirEntry) bool {
	return !de.IsDir()
}
