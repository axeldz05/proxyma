package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: go run merge_profiles.go <output> <profile1> <profile2> ...")
		os.Exit(1)
	}

	outputPath := os.Args[1]
	profilePaths := os.Args[2:]

	counts := make(map[string]int)
	var order []string
	mode := "set"

	for _, path := range profilePaths {
		file, err := os.Open(path)
		if err != nil {
			// Skip missing files
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "mode:") {
				m := strings.TrimSpace(strings.Split(line, ":")[1])
				if m != "" {
					mode = m
				}
				continue
			}
			parts := strings.Fields(line)
			if len(parts) != 3 {
				continue
			}
			block := parts[0] + " " + parts[1]
			count, err := strconv.Atoi(parts[2])
			if err != nil {
				continue
			}
			if _, exists := counts[block]; !exists {
				order = append(order, block)
			}
			if mode == "set" {
				if count > 0 {
					counts[block] = 1
				}
			} else {
				counts[block] += count
			}
		}
		_ = file.Close()
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		fmt.Printf("Error creating output file: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	writer := bufio.NewWriter(outFile)
	_, _ = writer.WriteString("mode: " + mode + "\n")
	for _, block := range order {
		_, _ = writer.WriteString(fmt.Sprintf("%s %d\n", block, counts[block]))
	}
	_ = writer.Flush()
	fmt.Printf("Successfully merged profiles into %s\n", outputPath)
}
