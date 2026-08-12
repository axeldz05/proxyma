package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxyma/internal/covermerge"
)

func TestRunUpdatesAndChecksBaseline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	profilePath := filepath.Join(dir, "coverage.out")
	baselinePath := filepath.Join(dir, "baseline.json")
	writeProfile(t, profilePath, `mode: set
proxyma/internal/production/code.go:1.1,2.1 1 1
proxyma/internal/covermerge/tool.go:1.1,2.1 1 0
`)
	if err := covermerge.WriteBaselineFile(baselinePath, covermerge.Baseline{
		Version:  covermerge.BaselineVersion,
		Epsilon:  covermerge.DefaultEpsilon,
		Packages: map[string]float64{},
		Exclusions: map[string]string{
			"proxyma/internal/covermerge": "coverage tooling; not shipped",
		},
	}); err != nil {
		t.Fatalf("WriteBaselineFile() error = %v", err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"update",
		baselinePath,
		profilePath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run(update) error = %v; stderr:\n%s", err, &stderr)
	}

	baseline, err := covermerge.ReadBaselineFile(baselinePath)
	if err != nil {
		t.Fatalf("ReadBaselineFile() error = %v", err)
	}
	if got := baseline.Packages["proxyma/internal/production"]; got != 100.0 {
		t.Fatalf("production floor = %.1f, want 100.0", got)
	}
	if _, tracked := baseline.Packages["proxyma/internal/covermerge"]; tracked {
		t.Fatal("tooling package is tracked, want excluded")
	}
	if got := baseline.Exclusions["proxyma/internal/covermerge"]; got == "" {
		t.Fatal("tooling exclusion reason is empty")
	}

	writeProfile(t, profilePath, `mode: set
proxyma/internal/production/code.go:1.1,2.1 1 1
proxyma/internal/covermerge/tool.go:1.1,2.1 1 0
proxyma/internal/newpkg/new.go:1.1,2.1 1 0
`)
	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"check",
		baselinePath,
		profilePath,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run(check) error = %v; stderr:\n%s", err, &stderr)
	}
	if !strings.Contains(stderr.String(), "WARNING: untracked package proxyma/internal/newpkg") {
		t.Fatalf("run(check) stderr = %q, want untracked-package warning", &stderr)
	}
}

func TestRunReportsMissingInputs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	profilePath := filepath.Join(dir, "coverage.out")
	baselinePath := filepath.Join(dir, "baseline.json")
	writeProfile(t, profilePath, `mode: set
proxyma/internal/production/code.go:1.1,2.1 1 1
`)
	if err := covermerge.WriteBaselineFile(baselinePath, covermerge.Baseline{
		Version:    covermerge.BaselineVersion,
		Epsilon:    covermerge.DefaultEpsilon,
		Packages:   map[string]float64{"proxyma/internal/production": 100},
		Exclusions: map[string]string{},
	}); err != nil {
		t.Fatalf("WriteBaselineFile() error = %v", err)
	}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "check baseline",
			args:    []string{"check", filepath.Join(dir, "missing-baseline.json"), profilePath},
			wantErr: "open coverage baseline",
		},
		{
			name:    "check profile",
			args:    []string{"check", baselinePath, filepath.Join(dir, "missing-profile.out")},
			wantErr: "open coverage profile",
		},
		{
			name:    "update profile",
			args:    []string{"update", filepath.Join(dir, "new-baseline.json"), filepath.Join(dir, "missing-profile.out")},
			wantErr: "open coverage profile",
		},
		{
			name:    "check arguments",
			args:    []string{"check", baselinePath},
			wantErr: "check: want <baseline> <profile>",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := run(test.args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("run(%q) error = %v, want containing %q", test.args, err, test.wantErr)
			}
		})
	}
}

func writeProfile(t *testing.T, path, profile string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
}
