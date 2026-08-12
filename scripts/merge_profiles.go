package main

import (
	"flag"
	"fmt"
	"os"

	"proxyma/internal/covermerge"
)

func main() {
	strict := flag.Bool("strict", false, "require every input profile to exist")
	flag.Usage = func() {
		_, _ = fmt.Fprintln(flag.CommandLine.Output(), "Usage: go run scripts/merge_profiles.go [--strict] <output> <profile1> [profile2 ...]")
	}
	flag.Parse()
	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}

	inputs := make([]covermerge.File, 0, flag.NArg()-1)
	for _, path := range flag.Args()[1:] {
		inputs = append(inputs, covermerge.File{Path: path, Required: *strict})
	}
	outputPath := flag.Arg(0)
	if err := covermerge.MergeFiles(outputPath, inputs); err != nil {
		fmt.Fprintf(os.Stderr, "merge coverage profiles: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Successfully merged profiles into %s\n", outputPath)
}
