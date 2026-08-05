package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	proxyma_bind "proxyma/cmd/proxyma-bind"
)

func launchEditor(pipelineID string, fileToOpen string) string {
	binaryPath := "/home/drusila/Projects/proxyma-services/editor/proxyma-editor"
	if _, err := os.Stat(binaryPath); err != nil {
		return proxyma_bind.BindErrorJSON(fmt.Errorf("Editor binary not found. Please compile it first: %v", err))
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

	err := cmd.Run()
	if err != nil {
		return proxyma_bind.BindErrorJSON(fmt.Errorf("Failed to run editor: %v", err))
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
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

type cliStreamListener struct {
	onChunkFunc func(chunk string)
	onDoneFunc  func()
}

func (l *cliStreamListener) OnChunk(chunkJSON string) {
	if l.onChunkFunc != nil {
		l.onChunkFunc(chunkJSON)
	}
}

func (l *cliStreamListener) OnError(errMsg string) {
	fmt.Fprintf(os.Stderr, "Stream Error: %s\n", errMsg)
	if l.onDoneFunc != nil {
		l.onDoneFunc()
	}
}

func (l *cliStreamListener) OnComplete() {
	if l.onDoneFunc != nil {
		l.onDoneFunc()
	}
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
