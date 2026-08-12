package covermerge_test

import (
	"bytes"
	"strings"
	"testing"

	"proxyma/internal/covermerge"
)

func TestParseProfileReportsStatementWeightedPackageCoverage(t *testing.T) {
	t.Parallel()

	profile := `mode: set
proxyma/internal/alpha/alpha.go:10.1,12.2 2 1
proxyma/internal/alpha/other.go:14.1,17.2 3 0
proxyma/internal/beta/beta.go:5.1,9.2 4 1
`
	got, err := covermerge.ParseProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatalf("ParseProfile() error = %v", err)
	}

	want := map[string]float64{
		"proxyma/internal/alpha": 40.0,
		"proxyma/internal/beta":  100.0,
	}
	if diff := coverageDiff(got, want); diff != "" {
		t.Fatalf("ParseProfile() mismatch (-got +want):\n%s", diff)
	}
}

func TestParseProfileRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		profile string
		wantErr string
	}{
		{
			name:    "empty",
			profile: "",
			wantErr: "missing coverage mode",
		},
		{
			name:    "unsupported mode",
			profile: "mode: mystery\n",
			wantErr: `unsupported coverage mode "mystery"`,
		},
		{
			name: "malformed block",
			profile: `mode: set
proxyma/internal/alpha/alpha.go:not-a-location 1 1
`,
			wantErr: "invalid block location",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := covermerge.ParseProfile(strings.NewReader(test.profile))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ParseProfile() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestCheckBaseline(t *testing.T) {
	t.Parallel()

	const tracked = "proxyma/internal/tracked"
	base := covermerge.Baseline{
		Version: covermerge.BaselineVersion,
		Epsilon: covermerge.DefaultEpsilon,
		Packages: map[string]float64{
			tracked: 80.0,
		},
		Exclusions: map[string]string{
			"proxyma/internal/testutil": "test support package; not shipped",
		},
	}

	t.Run("pass", func(t *testing.T) {
		t.Parallel()

		report, err := covermerge.Check(map[string]float64{tracked: 80.0}, base)
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if len(report.Regressions) != 0 || len(report.Missing) != 0 || len(report.Untracked) != 0 {
			t.Fatalf("Check() report = %#v, want empty", report)
		}
	})

	t.Run("drop", func(t *testing.T) {
		t.Parallel()

		report, err := covermerge.Check(map[string]float64{tracked: 79.8}, base)
		if err == nil {
			t.Fatal("Check() error = nil, want regression")
		}
		if len(report.Regressions) != 1 {
			t.Fatalf("Check() regressions = %#v, want one", report.Regressions)
		}
		got := report.Regressions[0]
		if got.Package != tracked || got.Current != 79.8 || got.Floor != 80.0 {
			t.Fatalf("Check() regression = %#v, want package=%q current=79.8 floor=80.0", got, tracked)
		}
	})

	t.Run("epsilon", func(t *testing.T) {
		t.Parallel()

		report, err := covermerge.Check(map[string]float64{tracked: 79.9}, base)
		if err != nil {
			t.Fatalf("Check() error = %v, want drop within epsilon to pass", err)
		}
		if len(report.Regressions) != 0 {
			t.Fatalf("Check() regressions = %#v, want none", report.Regressions)
		}
	})

	t.Run("missing package", func(t *testing.T) {
		t.Parallel()

		report, err := covermerge.Check(map[string]float64{}, base)
		if err == nil {
			t.Fatal("Check() error = nil, want missing package failure")
		}
		if len(report.Missing) != 1 || report.Missing[0] != tracked {
			t.Fatalf("Check() missing = %#v, want [%q]", report.Missing, tracked)
		}
	})

	t.Run("new package warning", func(t *testing.T) {
		t.Parallel()

		const newPackage = "proxyma/internal/newpkg"
		report, err := covermerge.Check(map[string]float64{
			tracked:    80.0,
			newPackage: 12.3,
		}, base)
		if err != nil {
			t.Fatalf("Check() error = %v, want warning-only result", err)
		}
		if len(report.Untracked) != 1 || report.Untracked[0] != newPackage {
			t.Fatalf("Check() untracked = %#v, want [%q]", report.Untracked, newPackage)
		}
	})

	t.Run("exclusion", func(t *testing.T) {
		t.Parallel()

		report, err := covermerge.Check(map[string]float64{
			tracked:                     80.0,
			"proxyma/internal/testutil": 0.0,
		}, base)
		if err != nil {
			t.Fatalf("Check() error = %v", err)
		}
		if len(report.Untracked) != 0 {
			t.Fatalf("Check() untracked = %#v, want excluded package ignored", report.Untracked)
		}
	})

	t.Run("exclusion requires reason", func(t *testing.T) {
		t.Parallel()

		invalid := base
		invalid.Exclusions = map[string]string{"proxyma/internal/testutil": "  "}
		if _, err := covermerge.Check(map[string]float64{tracked: 80.0}, invalid); err == nil {
			t.Fatal("Check() error = nil, want empty exclusion reason failure")
		}
	})
}

func TestCheckRegressionFixture(t *testing.T) {
	t.Parallel()

	baseline, err := covermerge.ReadBaselineFile("testdata/ratchet-baseline.json")
	if err != nil {
		t.Fatalf("ReadBaselineFile() error = %v", err)
	}
	measured, err := covermerge.ParseProfileFile("testdata/ratchet-regression.out")
	if err != nil {
		t.Fatalf("ParseProfileFile() error = %v", err)
	}

	report, err := covermerge.Check(measured, baseline)
	if err == nil {
		t.Fatal("Check() error = nil, want regression")
	}
	if len(report.Regressions) != 1 {
		t.Fatalf("Check() regressions = %#v, want one", report.Regressions)
	}
	regression := report.Regressions[0]
	if regression.Package != "proxyma/internal/example" || regression.Current != 0 || regression.Floor != 50 {
		t.Fatalf("Check() regression = %#v, want example at 0.0 below 50.0", regression)
	}
}

func TestReadBaselineValidatesInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantErr  string
		wantMaps bool
	}{
		{
			name:     "normalizes omitted maps",
			input:    `{"version":1,"epsilon":0.1}`,
			wantMaps: true,
		},
		{
			name:    "unknown field",
			input:   `{"version":1,"epsilon":0.1,"packages":{},"exclusions":{},"extra":true}`,
			wantErr: `unknown field "extra"`,
		},
		{
			name:    "unsupported version",
			input:   `{"version":2,"epsilon":0.1,"packages":{},"exclusions":{}}`,
			wantErr: "version 2 is unsupported",
		},
		{
			name:    "exclusion without reason",
			input:   `{"version":1,"epsilon":0.1,"packages":{},"exclusions":{"proxyma/internal/testutil":" "}}`,
			wantErr: "requires a reason",
		},
		{
			name:    "multiple values",
			input:   `{"version":1,"epsilon":0.1} {}`,
			wantErr: "multiple JSON values",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			baseline, err := covermerge.ReadBaseline(strings.NewReader(test.input))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ReadBaseline() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadBaseline() error = %v", err)
			}
			if test.wantMaps && (baseline.Packages == nil || baseline.Exclusions == nil) {
				t.Fatalf("ReadBaseline() maps = %#v, %#v; want non-nil", baseline.Packages, baseline.Exclusions)
			}
		})
	}
}

func TestUpdateBaselineIsTruncatedAndDeterministic(t *testing.T) {
	t.Parallel()

	first, err := covermerge.Update(
		map[string]float64{
			"proxyma/internal/zeta":      42.01,
			"proxyma/internal/telemetry": 0.0,
			"proxyma/internal/alpha":     83.99,
			"proxyma/internal/testutil":  100.0,
		},
		map[string]string{
			"proxyma/scripts":           "development tooling; not shipped",
			"proxyma/internal/testutil": "test support package; not shipped",
		},
	)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	second, err := covermerge.Update(
		map[string]float64{
			"proxyma/internal/testutil":  100.0,
			"proxyma/internal/alpha":     83.99,
			"proxyma/internal/telemetry": 0.0,
			"proxyma/internal/zeta":      42.01,
		},
		map[string]string{
			"proxyma/internal/testutil": "test support package; not shipped",
			"proxyma/scripts":           "development tooling; not shipped",
		},
	)
	if err != nil {
		t.Fatalf("Update() second error = %v", err)
	}

	var firstJSON, secondJSON bytes.Buffer
	if err := covermerge.WriteBaseline(&firstJSON, first); err != nil {
		t.Fatalf("WriteBaseline() error = %v", err)
	}
	if err := covermerge.WriteBaseline(&secondJSON, second); err != nil {
		t.Fatalf("WriteBaseline() second error = %v", err)
	}
	if firstJSON.String() != secondJSON.String() {
		t.Fatalf("WriteBaseline() is nondeterministic:\nfirst:\n%s\nsecond:\n%s", &firstJSON, &secondJSON)
	}

	const want = `{
  "version": 1,
  "epsilon": 0.1,
  "packages": {
    "proxyma/internal/alpha": 83.9,
    "proxyma/internal/telemetry": 0.0,
    "proxyma/internal/zeta": 42.0
  },
  "exclusions": {
    "proxyma/internal/testutil": "test support package; not shipped",
    "proxyma/scripts": "development tooling; not shipped"
  }
}
`
	if firstJSON.String() != want {
		t.Fatalf("WriteBaseline() mismatch:\ngot:\n%s\nwant:\n%s", &firstJSON, want)
	}

	loaded, err := covermerge.ReadBaseline(strings.NewReader(firstJSON.String()))
	if err != nil {
		t.Fatalf("ReadBaseline() error = %v", err)
	}
	if diff := coverageDiff(loaded.Packages, first.Packages); diff != "" {
		t.Fatalf("ReadBaseline() packages mismatch (-got +want):\n%s", diff)
	}
}

func coverageDiff(got, want map[string]float64) string {
	if len(got) != len(want) {
		return "different package counts"
	}
	for pkg, wantCoverage := range want {
		gotCoverage, ok := got[pkg]
		if !ok {
			return "missing " + pkg
		}
		if gotCoverage != wantCoverage {
			return pkg + " coverage differs"
		}
	}
	return ""
}
