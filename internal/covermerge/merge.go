// Package covermerge merges Go coverage profiles without emitting duplicate
// source blocks.
package covermerge

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var locationPattern = regexp.MustCompile(`^(.+):([1-9][0-9]*)\.([1-9][0-9]*),([1-9][0-9]*)\.([1-9][0-9]*)$`)

// File describes an input coverage profile. Missing optional files are skipped.
type File struct {
	Path     string
	Required bool
}

type block struct {
	path                string
	startLine, startCol uint64
	endLine, endCol     uint64
	statements, count   uint64
}

// MergeFiles merges files into outputPath. All present profiles must be valid
// and use the same coverage mode.
func MergeFiles(outputPath string, files []File) error {
	blocks := make(map[string]*block)
	var order []string
	var mode string
	loaded := 0

	for _, input := range files {
		file, err := os.Open(input.Path)
		if err != nil {
			if !input.Required && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("open coverage profile %q: %w", input.Path, err)
		}

		profileMode, err := mergeProfile(file, input.Path, blocks, &order)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return fmt.Errorf("close coverage profile %q: %w", input.Path, closeErr)
		}
		if mode == "" {
			mode = profileMode
		} else if profileMode != mode {
			return fmt.Errorf("coverage profile %q uses mode %q; expected %q", input.Path, profileMode, mode)
		}
		loaded++
	}

	if loaded == 0 {
		return errors.New("no coverage profiles to merge")
	}

	var output bytes.Buffer
	fmt.Fprintf(&output, "mode: %s\n", mode)
	for _, key := range order {
		item := blocks[key]
		fmt.Fprintf(
			&output,
			"%s:%d.%d,%d.%d %d %d\n",
			item.path,
			item.startLine,
			item.startCol,
			item.endLine,
			item.endCol,
			item.statements,
			item.count,
		)
	}
	if err := os.WriteFile(outputPath, output.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write merged coverage profile %q: %w", outputPath, err)
	}
	return nil
}

func mergeProfile(file *os.File, path string, blocks map[string]*block, order *[]string) (string, error) {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read coverage profile %q: %w", path, err)
		}
		return "", fmt.Errorf("%s:1: missing coverage mode", path)
	}
	mode, err := parseMode(scanner.Text())
	if err != nil {
		return "", fmt.Errorf("%s:1: %w", path, err)
	}

	for lineNumber := 2; scanner.Scan(); lineNumber++ {
		item, key, err := parseBlock(scanner.Text())
		if err != nil {
			return "", fmt.Errorf("%s:%d: %w", path, lineNumber, err)
		}
		if mode == "set" && item.count > 0 {
			item.count = 1
		}

		existing, duplicate := blocks[key]
		if !duplicate {
			blocks[key] = item
			*order = append(*order, key)
			continue
		}
		if existing.statements != item.statements {
			return "", fmt.Errorf(
				"%s:%d: block %s has %d statements; expected %d",
				path,
				lineNumber,
				key,
				item.statements,
				existing.statements,
			)
		}
		switch mode {
		case "set":
			if item.count > 0 {
				existing.count = 1
			}
		case "count", "atomic":
			if math.MaxUint64-existing.count < item.count {
				return "", fmt.Errorf("%s:%d: block %s count overflows uint64", path, lineNumber, key)
			}
			existing.count += item.count
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read coverage profile %q: %w", path, err)
	}
	return mode, nil
}

func parseMode(line string) (string, error) {
	const prefix = "mode:"
	if !strings.HasPrefix(line, prefix) {
		return "", errors.New("missing coverage mode")
	}
	mode := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	switch mode {
	case "set", "count", "atomic":
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported coverage mode %q", mode)
	}
}

func parseBlock(line string) (*block, string, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return nil, "", fmt.Errorf("invalid coverage line %q", line)
	}

	location := locationPattern.FindStringSubmatch(fields[0])
	if location == nil {
		return nil, "", fmt.Errorf("invalid block location %q", fields[0])
	}
	positions := make([]uint64, 4)
	for i, value := range location[2:] {
		position, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("invalid block location %q: %w", fields[0], err)
		}
		positions[i] = position
	}
	if positions[2] < positions[0] || (positions[2] == positions[0] && positions[3] < positions[1]) {
		return nil, "", fmt.Errorf("block ends before it starts in %q", fields[0])
	}

	statements, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("invalid statement count %q: %w", fields[1], err)
	}
	count, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("invalid execution count %q: %w", fields[2], err)
	}

	item := &block{
		path:       location[1],
		startLine:  positions[0],
		startCol:   positions[1],
		endLine:    positions[2],
		endCol:     positions[3],
		statements: statements,
		count:      count,
	}
	key := fmt.Sprintf(
		"%s:%d.%d,%d.%d",
		item.path,
		item.startLine,
		item.startCol,
		item.endLine,
		item.endCol,
	)
	return item, key, nil
}
