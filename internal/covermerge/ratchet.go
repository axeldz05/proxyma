package covermerge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"sort"
	"strings"
)

const (
	// BaselineVersion is the supported coverage baseline format.
	BaselineVersion = 1
	// DefaultEpsilon permits one tenth of a percentage point of measurement drift.
	DefaultEpsilon = 0.1
)

// Baseline records per-package coverage floors and intentional exclusions.
type Baseline struct {
	Version    int                `json:"version"`
	Epsilon    float64            `json:"epsilon"`
	Packages   map[string]float64 `json:"packages"`
	Exclusions map[string]string  `json:"exclusions"`
}

// Regression describes a package whose coverage fell below its floor.
type Regression struct {
	Package string
	Current float64
	Floor   float64
}

// Report contains deterministic details from a baseline check.
type Report struct {
	Regressions []Regression
	Missing     []string
	Untracked   []string
}

type packageStatements struct {
	total   uint64
	covered uint64
}

// ParseProfile calculates statement-weighted coverage percentages by package.
func ParseProfile(reader io.Reader) (map[string]float64, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read coverage profile: %w", err)
		}
		return nil, errors.New("coverage profile: missing coverage mode")
	}
	if _, err := parseMode(scanner.Text()); err != nil {
		return nil, fmt.Errorf("coverage profile:1: %w", err)
	}

	statements := make(map[string]packageStatements)
	for lineNumber := 2; scanner.Scan(); lineNumber++ {
		item, _, err := parseBlock(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("coverage profile:%d: %w", lineNumber, err)
		}

		packagePath := path.Dir(strings.ReplaceAll(item.path, `\`, "/"))
		current := statements[packagePath]
		if math.MaxUint64-current.total < item.statements {
			return nil, fmt.Errorf("coverage profile:%d: package %q statement count overflows uint64", lineNumber, packagePath)
		}
		current.total += item.statements
		if item.count > 0 {
			if math.MaxUint64-current.covered < item.statements {
				return nil, fmt.Errorf("coverage profile:%d: package %q covered count overflows uint64", lineNumber, packagePath)
			}
			current.covered += item.statements
		}
		statements[packagePath] = current
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read coverage profile: %w", err)
	}

	coverage := make(map[string]float64, len(statements))
	for packagePath, counts := range statements {
		if counts.total == 0 {
			coverage[packagePath] = 0
			continue
		}
		coverage[packagePath] = float64(counts.covered) * 100 / float64(counts.total)
	}
	return coverage, nil
}

// ParseProfileFile parses a Go coverage profile from path.
func ParseProfileFile(filePath string) (map[string]float64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open coverage profile %q: %w", filePath, err)
	}

	coverage, parseErr := ParseProfile(file)
	closeErr := file.Close()
	if parseErr != nil {
		return nil, fmt.Errorf("parse coverage profile %q: %w", filePath, parseErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close coverage profile %q: %w", filePath, closeErr)
	}
	return coverage, nil
}

// Check compares measured package coverage with a baseline. Untracked packages
// are warnings in the report; regressions and missing tracked packages fail.
func Check(measured map[string]float64, baseline Baseline) (Report, error) {
	var report Report
	if err := validateBaseline(baseline); err != nil {
		return report, err
	}
	if err := validateCoverage(measured); err != nil {
		return report, err
	}

	for packagePath, floor := range baseline.Packages {
		current, ok := measured[packagePath]
		if !ok {
			report.Missing = append(report.Missing, packagePath)
			continue
		}
		if current+baseline.Epsilon+1e-9 < floor {
			report.Regressions = append(report.Regressions, Regression{
				Package: packagePath,
				Current: current,
				Floor:   floor,
			})
		}
	}
	for packagePath := range measured {
		if _, tracked := baseline.Packages[packagePath]; tracked {
			continue
		}
		if _, excluded := baseline.Exclusions[packagePath]; excluded {
			continue
		}
		report.Untracked = append(report.Untracked, packagePath)
	}

	sort.Slice(report.Regressions, func(i, j int) bool {
		return report.Regressions[i].Package < report.Regressions[j].Package
	})
	sort.Strings(report.Missing)
	sort.Strings(report.Untracked)

	if len(report.Regressions) != 0 || len(report.Missing) != 0 {
		return report, fmt.Errorf(
			"coverage check failed: %d regression(s), %d missing package(s)",
			len(report.Regressions),
			len(report.Missing),
		)
	}
	return report, nil
}

// Update creates a baseline from measured coverage, truncating floors to one
// decimal place and omitting explicitly excluded packages.
func Update(measured map[string]float64, exclusions map[string]string) (Baseline, error) {
	baseline := Baseline{
		Version:    BaselineVersion,
		Epsilon:    DefaultEpsilon,
		Packages:   make(map[string]float64),
		Exclusions: cloneExclusions(exclusions),
	}
	if err := validateExclusions(baseline.Exclusions); err != nil {
		return Baseline{}, err
	}
	if err := validateCoverage(measured); err != nil {
		return Baseline{}, err
	}

	for packagePath, current := range measured {
		if _, excluded := baseline.Exclusions[packagePath]; excluded {
			continue
		}
		baseline.Packages[packagePath] = math.Trunc(current*10) / 10
	}
	return baseline, nil
}

// ReadBaseline decodes and validates a coverage baseline.
func ReadBaseline(reader io.Reader) (Baseline, error) {
	var baseline Baseline
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&baseline); err != nil {
		return Baseline{}, fmt.Errorf("decode coverage baseline: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Baseline{}, err
	}
	if baseline.Packages == nil {
		baseline.Packages = make(map[string]float64)
	}
	if baseline.Exclusions == nil {
		baseline.Exclusions = make(map[string]string)
	}
	if err := validateBaseline(baseline); err != nil {
		return Baseline{}, err
	}
	return baseline, nil
}

// ReadBaselineFile reads a coverage baseline from path.
func ReadBaselineFile(filePath string) (Baseline, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return Baseline{}, fmt.Errorf("open coverage baseline %q: %w", filePath, err)
	}

	baseline, readErr := ReadBaseline(file)
	closeErr := file.Close()
	if readErr != nil {
		return Baseline{}, fmt.Errorf("read coverage baseline %q: %w", filePath, readErr)
	}
	if closeErr != nil {
		return Baseline{}, fmt.Errorf("close coverage baseline %q: %w", filePath, closeErr)
	}
	return baseline, nil
}

type oneDecimal float64

func (value oneDecimal) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%.1f", value)), nil
}

type baselineJSON struct {
	Version    int                   `json:"version"`
	Epsilon    oneDecimal            `json:"epsilon"`
	Packages   map[string]oneDecimal `json:"packages"`
	Exclusions map[string]string     `json:"exclusions"`
}

// WriteBaseline writes a validated baseline as deterministic indented JSON.
func WriteBaseline(writer io.Writer, baseline Baseline) error {
	if err := validateBaseline(baseline); err != nil {
		return err
	}

	packages := make(map[string]oneDecimal, len(baseline.Packages))
	for packagePath, floor := range baseline.Packages {
		packages[packagePath] = oneDecimal(floor)
	}
	data, err := json.MarshalIndent(baselineJSON{
		Version:    baseline.Version,
		Epsilon:    oneDecimal(baseline.Epsilon),
		Packages:   packages,
		Exclusions: baseline.Exclusions,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode coverage baseline: %w", err)
	}
	data = append(data, '\n')
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("write coverage baseline: %w", err)
	}
	return nil
}

// WriteBaselineFile writes a baseline to path.
func WriteBaselineFile(filePath string, baseline Baseline) error {
	var output bytes.Buffer
	if err := WriteBaseline(&output, baseline); err != nil {
		return err
	}
	if err := os.WriteFile(filePath, output.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write coverage baseline %q: %w", filePath, err)
	}
	return nil
}

func validateBaseline(baseline Baseline) error {
	if baseline.Version != BaselineVersion {
		return fmt.Errorf("coverage baseline version %d is unsupported; want %d", baseline.Version, BaselineVersion)
	}
	if !validPercentage(baseline.Epsilon) {
		return fmt.Errorf("coverage baseline epsilon %.4g is outside [0, 100]", baseline.Epsilon)
	}
	if err := validateCoverage(baseline.Packages); err != nil {
		return fmt.Errorf("invalid coverage baseline: %w", err)
	}
	if err := validateExclusions(baseline.Exclusions); err != nil {
		return err
	}
	for packagePath := range baseline.Packages {
		if _, excluded := baseline.Exclusions[packagePath]; excluded {
			return fmt.Errorf("coverage package %q is both tracked and excluded", packagePath)
		}
	}
	return nil
}

func validateCoverage(coverage map[string]float64) error {
	for packagePath, percentage := range coverage {
		if strings.TrimSpace(packagePath) == "" {
			return errors.New("coverage package path is empty")
		}
		if !validPercentage(percentage) {
			return fmt.Errorf("coverage for package %q is %.4g; want a value in [0, 100]", packagePath, percentage)
		}
	}
	return nil
}

func validateExclusions(exclusions map[string]string) error {
	for packagePath, reason := range exclusions {
		if strings.TrimSpace(packagePath) == "" {
			return errors.New("coverage exclusion package path is empty")
		}
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("coverage exclusion %q requires a reason", packagePath)
		}
	}
	return nil
}

func validPercentage(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func cloneExclusions(exclusions map[string]string) map[string]string {
	cloned := make(map[string]string, len(exclusions))
	for packagePath, reason := range exclusions {
		cloned[packagePath] = reason
	}
	return cloned
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("decode coverage baseline: multiple JSON values")
		}
		return fmt.Errorf("decode coverage baseline: %w", err)
	}
	return nil
}
