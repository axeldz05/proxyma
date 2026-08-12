package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	proxyma_bind "proxyma/cmd/proxyma-bind"
	"proxyma/internal/utils"
)

func resolveEditorBinary() (string, error) {
	if env := os.Getenv("PROXYMA_EDITOR"); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("PROXYMA_EDITOR=%q not found: %w", env, err)
		}
		return env, nil
	}
	if p, err := exec.LookPath("proxyma-editor"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("editor binary not found: set PROXYMA_EDITOR or place proxyma-editor on PATH")
}

func launchEditor(pipelineID string, fileToOpen string) string {
	binaryPath, err := resolveEditorBinary()
	if err != nil {
		return proxyma_bind.BindErrorJSON(err)
	}

	cmdArgs := []string{"--storage", cliStorage}
	if pipelineID != "" {
		cmdArgs = append(cmdArgs, "--id", pipelineID)
	}
	if fileToOpen != "" {
		cmdArgs = append(cmdArgs, "--file", fileToOpen)
	}

	cmd := exec.Command(binaryPath, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err = cmd.Run()
	if err != nil {
		return proxyma_bind.BindErrorJSON(fmt.Errorf("failed to run editor: %v", err))
	}
	return proxyma_bind.BindMessageJSON("Editor closed")
}

func isTerminalInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func fileExists(path string) bool {
	return utils.FileExists(path)
}

// resolveExistingJSONPath returns candidate when it looks like an existing .json schema path.
func resolveExistingJSONPath(candidate string) string {
	if candidate == "" {
		return ""
	}
	if strings.HasSuffix(candidate, ".json") || fileExists(candidate) {
		return candidate
	}
	return ""
}

func openFileWithSystemDefault(storageDir string, name string, localBlobPath string) (string, error) {
	if _, err := os.Stat(localBlobPath); err != nil {
		return "", fmt.Errorf("local cache file not found: %w", err)
	}

	previewDir := filepath.Join(storageDir, "preview")
	_ = os.MkdirAll(previewDir, 0755)

	targetPath := filepath.Join(previewDir, filepath.Base(name))
	_ = os.Remove(targetPath)

	// Symlink to preserve file extension
	if err := os.Symlink(localBlobPath, targetPath); err != nil {
		src, err := os.Open(localBlobPath)
		if err == nil {
			dst, err := os.Create(targetPath)
			if err == nil {
				_, _ = io.Copy(dst, src)
				_ = dst.Close()
			}
			_ = src.Close()
		}
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", targetPath)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", targetPath)
	default:
		cmd = exec.Command("xdg-open", targetPath)
	}

	if err := cmd.Start(); err != nil {
		return targetPath, fmt.Errorf("failed to launch system viewer (%s): %w", cmd.Path, err)
	}
	return targetPath, nil
}
