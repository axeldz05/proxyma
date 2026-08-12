// Command coverage-ratchet lives under cmd instead of scripts because scripts
// already contains a package-main entrypoint; a second main would break go test ./....
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"proxyma/internal/covermerge"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		if err := printUsage(stderr); err != nil {
			return err
		}
		return errors.New("missing command")
	}

	switch args[0] {
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		return printUsage(stdout)
	default:
		if err := printUsage(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runCheck(args []string, stdout, stderr io.Writer) error {
	if len(args) != 2 {
		return errors.New("check: want <baseline> <profile>")
	}
	baselinePath, profilePath := args[0], args[1]

	baseline, err := covermerge.ReadBaselineFile(baselinePath)
	if err != nil {
		return err
	}
	measured, err := covermerge.ParseProfileFile(profilePath)
	if err != nil {
		return err
	}
	report, checkErr := covermerge.Check(measured, baseline)
	for _, packagePath := range report.Untracked {
		if err := writeOutput(stderr, "WARNING: untracked package %s\n", packagePath); err != nil {
			return err
		}
	}
	for _, regression := range report.Regressions {
		if err := writeOutput(
			stderr,
			"FAIL: %s coverage %.2f%% is below floor %.1f%% (epsilon %.1f)\n",
			regression.Package,
			regression.Current,
			regression.Floor,
			baseline.Epsilon,
		); err != nil {
			return err
		}
	}
	for _, packagePath := range report.Missing {
		if err := writeOutput(stderr, "FAIL: tracked package %s is missing from coverage profile\n", packagePath); err != nil {
			return err
		}
	}
	if checkErr != nil {
		return checkErr
	}

	return writeOutput(stdout, "coverage ratchet passed: %d tracked package(s)\n", len(baseline.Packages))
}

func runUpdate(args []string, stdout, stderr io.Writer) error {
	if len(args) != 2 {
		return errors.New("update: want <baseline> <profile>")
	}
	baselinePath, profilePath := args[0], args[1]

	exclusions := make(map[string]string)
	existing, err := covermerge.ReadBaselineFile(baselinePath)
	if err == nil {
		for packagePath, reason := range existing.Exclusions {
			exclusions[packagePath] = reason
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	measured, err := covermerge.ParseProfileFile(profilePath)
	if err != nil {
		return err
	}
	baseline, err := covermerge.Update(measured, exclusions)
	if err != nil {
		return err
	}
	if err := covermerge.WriteBaselineFile(baselinePath, baseline); err != nil {
		return err
	}

	return writeOutput(
		stdout,
		"updated coverage baseline: %d tracked, %d excluded package(s)\n",
		len(baseline.Packages),
		len(baseline.Exclusions),
	)
}

func printUsage(writer io.Writer) error {
	return writeOutput(
		writer,
		"Usage:\n  coverage-ratchet check <baseline> <profile>\n"+
			"  coverage-ratchet update <baseline> <profile>\n",
	)
}

func writeOutput(writer io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(writer, format, args...); err != nil {
		return fmt.Errorf("write command output: %w", err)
	}
	return nil
}
