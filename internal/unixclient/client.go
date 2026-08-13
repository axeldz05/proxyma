package unixclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

const (
	DefaultResponseIdleTimeout = protocol.RPCTimeoutTaskWait
	maxUnaryResponseBytes      = 32 << 20
)

type unavailableError struct {
	err error
}

func (e *unavailableError) Error() string { return e.err.Error() }
func (e *unavailableError) Unwrap() error { return e.err }

// IsUnavailable reports whether the daemon socket does not exist or refused
// the connection. Other I/O failures must not trigger an offline fallback.
func IsUnavailable(err error) bool {
	var unavailable *unavailableError
	return errors.As(err, &unavailable)
}

// IsUnavailableDialError classifies only absence/refusal as safe for offline fallback.
func IsUnavailableDialError(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// CanonicalFilesystemPath resolves symlinks through the longest existing path
// prefix while preserving any missing suffix.
func CanonicalFilesystemPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	current := absolute
	var missing []string
	for {
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		missing = append(missing, filepath.Base(current))
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
	}
	return filepath.Clean(absolute)
}

// Dial opens the daemon socket after resolving a configured storage-path alias.
func Dial(storagePath string) (net.Conn, error) {
	socketStorage := storagePath
	cfg, err := protocol.LoadConfig(storagePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("couldn't load config: %w", err)
		}
	} else if cfg.StoragePath != "" {
		socketStorage = CanonicalFilesystemPath(cfg.StoragePath)
	}
	conn, err := net.Dial("unix", protocol.UnixSockPath(socketStorage))
	if err == nil {
		return conn, nil
	}
	dialErr := fmt.Errorf("daemon is unreachable. Is 'proxyma run' active? Error: %w", err)
	if IsUnavailableDialError(err) {
		return nil, &unavailableError{err: dialErr}
	}
	return nil, dialErr
}

// WriteRequest marshals and writes a unary Unix request.
func WriteRequest(conn net.Conn, action string, args map[string]string) error {
	return writeRequest(conn, protocol.UnixRequest{Action: action, Args: args})
}

// WriteStreamRequest advertises the supported stream framing version.
func WriteStreamRequest(conn net.Conn, action string, args map[string]string) error {
	return writeRequest(conn, protocol.UnixRequest{
		Action:         action,
		Args:           args,
		StreamVersions: []int{protocol.ServiceStreamVersion},
	})
}

func writeRequest(conn net.Conn, req protocol.UnixRequest) error {
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	if _, err := conn.Write(reqBytes); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}
	return nil
}

// ReadResponse reads one unary response using the standard idle timeout.
func ReadResponse(conn net.Conn) (protocol.UnixResponse, error) {
	return ReadResponseWithIdleTimeout(conn, DefaultResponseIdleTimeout)
}

// ReadResponseWithIdleTimeout reads one bounded unary response. The explicit
// timeout keeps the primitive testable without global package state.
func ReadResponseWithIdleTimeout(conn net.Conn, idleTimeout time.Duration) (protocol.UnixResponse, error) {
	var resp protocol.UnixResponse
	var respBytes []byte
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
		n, err := conn.Read(buf)
		if n > 0 {
			respBytes = append(respBytes, buf[:n]...)
			if len(respBytes) > maxUnaryResponseBytes {
				return resp, fmt.Errorf("daemon response exceeds 32MB")
			}
		}
		if err != nil {
			break
		}
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return resp, fmt.Errorf("failed to parse daemon response: %w", err)
	}
	return resp, nil
}

// ScanNDJSON scans line-delimited stream response frames.
func ScanNDJSON(conn net.Conn, onLine func(protocol.UnixResponse) bool) error {
	var parseErr error
	err := utils.ScanNDJSON(conn, func(line []byte) bool {
		var resp protocol.UnixResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			parseErr = fmt.Errorf("invalid NDJSON stream chunk: %w", err)
			return false
		}
		return onLine(resp)
	})
	if parseErr != nil {
		return parseErr
	}
	return err
}

// CallUnary composes the public dial/write/read primitives for one request.
func CallUnary(storagePath, action string, args map[string]string) (json.RawMessage, error) {
	return CallUnaryWithIdleTimeout(storagePath, action, args, DefaultResponseIdleTimeout)
}

func CallUnaryWithIdleTimeout(
	storagePath string,
	action string,
	args map[string]string,
	idleTimeout time.Duration,
) (json.RawMessage, error) {
	conn, err := Dial(storagePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if err := WriteRequest(conn, action, args); err != nil {
		return nil, err
	}
	resp, err := ReadResponseWithIdleTimeout(conn, idleTimeout)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return resp.Data, nil
}
