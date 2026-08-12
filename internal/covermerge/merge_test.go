package covermerge_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyma/internal/covermerge"
)

func TestMergeFilesZeroCountDuplicateRegression(t *testing.T) {
	t.Parallel()

	got := mergeFixtures(t, "zero-first.out", "zero-second.out")
	const want = `mode: set
proxyma/internal/example.go:10.2,12.3 2 1
`
	if got != want {
		t.Fatalf("merged profile mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if count := strings.Count(got, "proxyma/internal/example.go:10.2,12.3"); count != 1 {
		t.Fatalf("duplicate block count = %d, want 1", count)
	}
}

func TestMergeFilesSetUnion(t *testing.T) {
	t.Parallel()

	got := mergeFixtures(t, "set-first.out", "set-second.out")
	const want = `mode: set
proxyma/internal/shared.go:5.1,6.2 1 1
proxyma/internal/a.go:2.1,4.2 2 1
proxyma/internal/b.go:7.3,9.4 1 1
`
	if got != want {
		t.Fatalf("merged profile mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMergeFilesCountMode(t *testing.T) {
	t.Parallel()

	got := mergeFixtures(t, "count-first.out", "count-second.out")
	const want = `mode: count
proxyma/internal/shared.go:5.1,6.2 1 8
proxyma/internal/a.go:2.1,4.2 2 4
`
	if got != want {
		t.Fatalf("merged profile mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMergeFilesPreservesFirstSeenBlockOrder(t *testing.T) {
	t.Parallel()

	got := mergeFixtures(t, "order-first.out", "order-second.out")
	const want = `mode: set
proxyma/internal/z.go:10.1,11.2 1 1
proxyma/internal/zero.go:8.1,9.2 1 1
proxyma/internal/a.go:20.1,21.2 1 1
proxyma/internal/a.go:3.1,4.2 1 1
`
	if got != want {
		t.Fatalf("merged profile mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if count := strings.Count(got, "proxyma/internal/zero.go:8.1,9.2"); count != 1 {
		t.Fatalf("zero-count block occurrences = %d, want 1", count)
	}
}

func TestMergeFilesRejectsInvalidLines(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{
		"invalid-fields.out",
		"invalid-count.out",
		"invalid-location.out",
	} {
		fixture := fixture
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()

			output := filepath.Join(t.TempDir(), "coverage.out")
			err := covermerge.MergeFiles(output, []covermerge.File{{
				Path:     filepath.Join("testdata", fixture),
				Required: true,
			}})
			if err == nil {
				t.Fatal("MergeFiles() error = nil, want malformed profile error")
			}
			if !strings.Contains(err.Error(), fixture+":2") {
				t.Fatalf("MergeFiles() error = %q, want fixture and line number", err)
			}
		})
	}
}

func TestMergeFilesHandlesRequiredAndOptionalMissingFiles(t *testing.T) {
	t.Parallel()

	t.Run("required", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		output := filepath.Join(dir, "coverage.out")
		missing := filepath.Join(dir, "missing.out")
		err := covermerge.MergeFiles(output, []covermerge.File{{Path: missing, Required: true}})
		if err == nil {
			t.Fatal("MergeFiles() error = nil, want missing required file error")
		}
		if !strings.Contains(err.Error(), missing) {
			t.Fatalf("MergeFiles() error = %q, want missing path", err)
		}
		if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
			t.Fatalf("output should not exist after input failure; stat error = %v", statErr)
		}
	})

	t.Run("optional", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		output := filepath.Join(dir, "coverage.out")
		files := []covermerge.File{
			{Path: filepath.Join(dir, "missing.out")},
			{Path: filepath.Join("testdata", "zero-second.out"), Required: true},
		}
		if err := covermerge.MergeFiles(output, files); err != nil {
			t.Fatalf("MergeFiles() error = %v", err)
		}
		got, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		const want = `mode: set
proxyma/internal/example.go:10.2,12.3 2 1
`
		if string(got) != want {
			t.Fatalf("merged profile mismatch:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})
}

func mergeFixtures(t *testing.T, names ...string) string {
	t.Helper()

	files := make([]covermerge.File, 0, len(names))
	for _, name := range names {
		files = append(files, covermerge.File{
			Path:     filepath.Join("testdata", name),
			Required: true,
		})
	}
	output := filepath.Join(t.TempDir(), "coverage.out")
	if err := covermerge.MergeFiles(output, files); err != nil {
		t.Fatalf("MergeFiles() error = %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}
