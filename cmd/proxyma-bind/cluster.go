package proxyma_bind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
)

var (
	joinClusterRemote = p2p.JoinCluster
	joinRename        = os.Rename
	startJoinedNode   = startNode
	joinMutex         sync.Mutex
)

const (
	joinJournalFileName = ".join-transaction.json"
	joinPhaseStaged     = "staged"
	joinPhaseInstalling = "installing"
	joinPhaseInstalled  = "installed"
	joinPhaseCommitted  = "committed"
)

type nodeGlobalsSnapshot struct {
	storage string
	logger  *slog.Logger
	running bool
}

// GenerateInviteToken creates an invite token valid for 15 minutes.
// Failures are BindErrorJSON. Success peels formatActionResult's BindMessageJSON
// envelope back to a raw token string so Android/CLI callers keep a stable wire shape.
func GenerateInviteToken() string {
	raw := InvokeDomainAction("cluster", "invite", nil)
	if IsBindError(raw) {
		return raw
	}
	// Prefer message envelope; fall back to raw / JSON-encoded token.
	var msgEnv struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &msgEnv); err == nil && msgEnv.Message != "" {
		return msgEnv.Message
	}
	var token string
	if err := json.Unmarshal([]byte(raw), &token); err == nil {
		return token
	}
	return raw
}

// JoinCluster joins an existing cluster, writes configuration, and starts the node.
func JoinCluster(storagePath string, token string, nodeID string, port string) (result string) {
	joinMutex.Lock()
	defer joinMutex.Unlock()

	previous := snapshotNodeGlobals()
	previousStopped := false
	joined := false
	defer func() {
		if joined || result == "" {
			return
		}
		if restoreErr := restorePreviousNode(previous, previousStopped); restoreErr != nil {
			result = bindErrorJSON(errors.Join(errors.New(ParseBindError(result)), restoreErr))
		}
	}()

	storagePath = canonicalStoragePath(storagePath)
	if err := recoverJoinInstallation(storagePath); err != nil {
		return bindErrorJSON(fmt.Errorf("failed to recover interrupted join: %w", err))
	}
	SetStoragePath(storagePath)
	writer := &protocol.LogWriter{Stdout: os.Stdout}
	logger := protocol.NewLogger(writer, true)
	srvMutex.Lock()
	appLogger = logger
	srvMutex.Unlock()

	token = strings.TrimSpace(token)
	token = strings.Trim(token, "\"'")
	if token == "" {
		return bindErrorJSON(fmt.Errorf("smart token is required"))
	}
	invitePayload, _, _ := p2p.ParseSmartToken(token)

	if nodeID == "" {
		nodeID = utils.GenerateDefaultNodeID()
	}
	if port == "" {
		port = protocol.DefaultTCPPort
	}

	// Auto load or generate configuration first
	var cfg protocol.NodeConfig
	if c, err := protocol.LoadConfig(storagePath); err == nil {
		cfg = c
	} else {
		// Default config values
		cfg = protocol.NodeConfig{
			Workers:     4,
			StoragePath: storagePath,
		}
	}

	// Prefer stable node-ID hostname (matches `proxyma init` and Docker Compose DNS).
	// Ephemeral bridge/LAN IPs are added later by AnnouncePresence as secondary addresses.
	localAddr := protocol.HTTPSAddr(nodeID, port)

	logFn := func(msg string, err error) {
		if err != nil {
			logger.Error(msg, "error", err)
		} else {
			logger.Info(msg)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), protocol.RPCTimeoutTaskWait)
	defer cancel()

	caCert, cert, privKeyPEM, successfulAddr, err := joinClusterRemote(ctx, token, nodeID, localAddr, logFn)
	if err != nil {
		return bindErrorJSON(fmt.Errorf("join failed: %w", err))
	}

	workersCount := cfg.Workers
	if workersCount <= 0 {
		workersCount = 4
	}

	bootstrap := successfulAddr
	if u, err := url.Parse(successfulAddr); err == nil && u.Hostname() == "0.0.0.0" {
		bootstrapPort := u.Port()
		if bootstrapPort == "" {
			bootstrapPort = protocol.DefaultTCPPort
		}
		sponsorHost := invitePayload.SponsorID
		if sponsorHost == "" {
			sponsorHost = firstRoutableInviteHost(invitePayload.Addresses)
		}
		if sponsorHost == "" {
			return bindErrorJSON(fmt.Errorf("join sponsor returned wildcard bootstrap without a sponsor identity or address"))
		}
		bootstrap = protocol.HTTPSAddr(sponsorHost, bootstrapPort)
	}

	newCfg := protocol.NodeConfig{
		ID:            nodeID,
		Address:       localAddr,
		StoragePath:   storagePath,
		Workers:       workersCount,
		CAPath:        filepath.Join(storagePath, "certs", "ca.crt"),
		BootstrapNode: bootstrap,
	}

	installation, err := stageJoinInstallation(
		storagePath,
		nodeID,
		newCfg,
		[]byte(caCert),
		[]byte(cert),
		privKeyPEM,
	)
	if err != nil {
		return bindErrorJSON(err)
	}
	defer installation.cleanupUncommitted()

	if stopResult := StopNodeWithError(); stopResult != "" {
		return stopResult
	}
	previousStopped = previous.running
	if err := installation.install(); err != nil {
		rollbackErr := installation.rollback()
		return bindErrorJSON(errors.Join(
			fmt.Errorf("failed to install joined certificates and config: %w", err),
			rollbackErr,
		))
	}

	startErr := startJoinedNode(storagePath, true)
	if startErr != "" {
		StopNode()
		rollbackErr := installation.rollback()
		startFailure := errors.New(ParseBindError(startErr))
		return bindErrorJSON(errors.Join(startFailure, rollbackErr))
	}
	if err := installation.commit(); err != nil {
		StopNode()
		rollbackErr := installation.rollback()
		return bindErrorJSON(errors.Join(fmt.Errorf("failed to commit joined node state: %w", err), rollbackErr))
	}
	if err := installation.cleanupCommitted(); err != nil {
		logger.Warn("Joined node committed; transaction cleanup will retry on startup", "error", err)
		if retryErr := installation.cleanupCommitted(); retryErr != nil {
			logger.Warn("Joined transaction cleanup retry failed", "error", retryErr)
		}
	}

	srvMutex.RLock()
	startedServer := srv
	srvMutex.RUnlock()
	if startedServer != nil {
		startNodeBackgroundWork(startedServer, func(ctx context.Context) {
			runDelayedJoinSync(ctx, startedServer)
		})
	}

	joined = true
	return ""
}

type joinInstallation struct {
	root            string
	stagedCerts     string
	stagedConfig    string
	finalCerts      string
	finalConfig     string
	backupCerts     string
	backupConfig    string
	certsBackedUp   bool
	configBackedUp  bool
	certsInstalled  bool
	configInstalled bool
	finished        bool
	phase           string
	journalPath     string
	hadCerts        bool
	hadConfig       bool
}

type joinJournal struct {
	Phase           string `json:"phase"`
	Root            string `json:"root"`
	StagedCerts     string `json:"staged_certs"`
	StagedConfig    string `json:"staged_config"`
	FinalCerts      string `json:"final_certs"`
	FinalConfig     string `json:"final_config"`
	BackupCerts     string `json:"backup_certs"`
	BackupConfig    string `json:"backup_config"`
	CertsBackedUp   bool   `json:"certs_backed_up"`
	ConfigBackedUp  bool   `json:"config_backed_up"`
	CertsInstalled  bool   `json:"certs_installed"`
	ConfigInstalled bool   `json:"config_installed"`
	HadCerts        bool   `json:"had_certs"`
	HadConfig       bool   `json:"had_config"`
}

func stageJoinInstallation(
	storagePath string,
	nodeID string,
	cfg protocol.NodeConfig,
	caPEM []byte,
	certPEM []byte,
	keyPEM []byte,
) (*joinInstallation, error) {
	if err := os.MkdirAll(storagePath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create join storage directory: %w", err)
	}
	root, err := os.MkdirTemp(storagePath, ".join-txn-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create join transaction: %w", err)
	}
	installation := &joinInstallation{
		root:         root,
		stagedCerts:  filepath.Join(root, "state", "certs"),
		stagedConfig: filepath.Join(root, "state", "config.json"),
		finalCerts:   filepath.Join(storagePath, "certs"),
		finalConfig:  filepath.Join(storagePath, "config.json"),
		backupCerts:  filepath.Join(root, "backup", "certs"),
		backupConfig: filepath.Join(root, "backup", "config.json"),
		phase:        joinPhaseStaged,
		journalPath:  filepath.Join(storagePath, joinJournalFileName),
	}
	installation.hadCerts, err = pathExists(installation.finalCerts)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	installation.hadConfig, err = pathExists(installation.finalConfig)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := os.MkdirAll(installation.stagedCerts, 0o755); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("failed to create staged certs directory: %w", err)
	}
	caPath, _ := p2p.CACertPaths(installation.stagedCerts)
	certPath, keyPath := p2p.NodeCertPaths(installation.stagedCerts, nodeID)
	if err := p2p.WriteNodePEMs(caPath, certPath, keyPath, caPEM, certPEM, keyPEM); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("failed to stage joined node PEMs: %w", err)
	}
	if err := utils.WriteJSONFile(installation.stagedConfig, cfg); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("failed to stage joined node config: %w", err)
	}
	for _, path := range []string{caPath, certPath, keyPath, installation.stagedConfig} {
		if err := syncFile(path); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("sync staged join state: %w", err)
		}
	}
	if err := syncDir(installation.stagedCerts); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	if err := installation.persistJournal(); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("persist staged join journal: %w", err)
	}
	return installation, nil
}

func (j *joinInstallation) install() error {
	j.phase = joinPhaseInstalling
	if err := j.persistJournal(); err != nil {
		return fmt.Errorf("persist join install phase: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(j.backupCerts), 0o755); err != nil {
		return fmt.Errorf("create join backup directory: %w", err)
	}
	if exists, err := pathExists(j.finalCerts); err != nil {
		return err
	} else if exists {
		if err := joinRename(j.finalCerts, j.backupCerts); err != nil {
			return fmt.Errorf("backup existing certificates: %w", err)
		}
		j.certsBackedUp = true
		if err := j.persistJournal(); err != nil {
			return err
		}
	}
	if exists, err := pathExists(j.finalConfig); err != nil {
		return err
	} else if exists {
		if err := joinRename(j.finalConfig, j.backupConfig); err != nil {
			return fmt.Errorf("backup existing config: %w", err)
		}
		j.configBackedUp = true
		if err := j.persistJournal(); err != nil {
			return err
		}
	}
	if err := joinRename(j.stagedCerts, j.finalCerts); err != nil {
		return fmt.Errorf("install joined certificates: %w", err)
	}
	j.certsInstalled = true
	if err := j.persistJournal(); err != nil {
		return err
	}
	if err := joinRename(j.stagedConfig, j.finalConfig); err != nil {
		return fmt.Errorf("install joined config: %w", err)
	}
	j.configInstalled = true
	j.phase = joinPhaseInstalled
	return j.persistJournal()
}

func (j *joinInstallation) rollback() error {
	var rollbackErr error
	if j.configInstalled {
		if err := os.RemoveAll(j.finalConfig); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove joined config: %w", err))
		}
		j.configInstalled = false
	}
	if j.certsInstalled {
		if err := os.RemoveAll(j.finalCerts); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove joined certificates: %w", err))
		}
		j.certsInstalled = false
	}
	if j.configBackedUp {
		if err := joinRename(j.backupConfig, j.finalConfig); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore previous config: %w", err))
		} else {
			j.configBackedUp = false
		}
	}
	if j.certsBackedUp {
		if err := joinRename(j.backupCerts, j.finalCerts); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore previous certificates: %w", err))
		} else {
			j.certsBackedUp = false
		}
	}
	if rollbackErr == nil {
		j.finished = true
		rollbackErr = errors.Join(os.RemoveAll(j.root), os.Remove(j.journalPath))
		if os.IsNotExist(rollbackErr) {
			rollbackErr = nil
		}
		if syncErr := syncDir(filepath.Dir(j.journalPath)); syncErr != nil {
			rollbackErr = errors.Join(rollbackErr, syncErr)
		}
	}
	return rollbackErr
}

func (j *joinInstallation) commit() error {
	j.phase = joinPhaseCommitted
	if err := j.persistJournal(); err != nil {
		return fmt.Errorf("persist committed join phase: %w", err)
	}
	j.finished = true
	return nil
}

func (j *joinInstallation) cleanupCommitted() error {
	rootErr := os.RemoveAll(j.root)
	journalErr := os.Remove(j.journalPath)
	if os.IsNotExist(journalErr) {
		journalErr = nil
	}
	return errors.Join(rootErr, journalErr, syncDir(filepath.Dir(j.journalPath)))
}

func (j *joinInstallation) cleanupUncommitted() {
	if !j.finished && !j.certsBackedUp && !j.configBackedUp {
		_ = os.RemoveAll(j.root)
		_ = os.Remove(j.journalPath)
		_ = syncDir(filepath.Dir(j.journalPath))
	}
}

func (j *joinInstallation) persistJournal() error {
	for _, dir := range []string{
		filepath.Dir(j.stagedCerts),
		filepath.Dir(j.backupCerts),
		filepath.Dir(j.finalCerts),
	} {
		if exists, _ := pathExists(dir); exists {
			if err := syncDir(dir); err != nil {
				return err
			}
		}
	}
	journal := joinJournal{
		Phase:           j.phase,
		Root:            j.root,
		StagedCerts:     j.stagedCerts,
		StagedConfig:    j.stagedConfig,
		FinalCerts:      j.finalCerts,
		FinalConfig:     j.finalConfig,
		BackupCerts:     j.backupCerts,
		BackupConfig:    j.backupConfig,
		CertsBackedUp:   j.certsBackedUp,
		ConfigBackedUp:  j.configBackedUp,
		CertsInstalled:  j.certsInstalled,
		ConfigInstalled: j.configInstalled,
		HadCerts:        j.hadCerts,
		HadConfig:       j.hadConfig,
	}
	return utils.WriteJSONFile(j.journalPath, journal)
}

func recoverJoinInstallation(storagePath string) error {
	storagePath = canonicalFilesystemPath(storagePath)
	journalPath := filepath.Join(storagePath, joinJournalFileName)
	var journal joinJournal
	if err := utils.ReadJSONFile(journalPath, &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read join journal: %w", err)
	}
	if journal.FinalCerts != filepath.Join(storagePath, "certs") ||
		journal.FinalConfig != filepath.Join(storagePath, "config.json") ||
		filepath.Dir(journal.Root) != storagePath ||
		!strings.HasPrefix(filepath.Base(journal.Root), ".join-txn-") ||
		journal.StagedCerts != filepath.Join(journal.Root, "state", "certs") ||
		journal.StagedConfig != filepath.Join(journal.Root, "state", "config.json") ||
		journal.BackupCerts != filepath.Join(journal.Root, "backup", "certs") ||
		journal.BackupConfig != filepath.Join(journal.Root, "backup", "config.json") {
		return fmt.Errorf("join journal paths do not belong to storage %q", storagePath)
	}
	installation := &joinInstallation{
		root:            journal.Root,
		stagedCerts:     journal.StagedCerts,
		stagedConfig:    journal.StagedConfig,
		finalCerts:      journal.FinalCerts,
		finalConfig:     journal.FinalConfig,
		backupCerts:     journal.BackupCerts,
		backupConfig:    journal.BackupConfig,
		certsBackedUp:   journal.CertsBackedUp,
		configBackedUp:  journal.ConfigBackedUp,
		certsInstalled:  journal.CertsInstalled,
		configInstalled: journal.ConfigInstalled,
		phase:           journal.Phase,
		journalPath:     journalPath,
		hadCerts:        journal.HadCerts,
		hadConfig:       journal.HadConfig,
	}
	if journal.Phase == joinPhaseCommitted {
		installation.finished = true
		return installation.cleanupCommitted()
	}
	if journal.Phase == joinPhaseStaged {
		installation.finished = true
		return installation.cleanupCommitted()
	}

	backupCertsExist, err := pathExists(installation.backupCerts)
	if err != nil {
		return err
	}
	backupConfigExists, err := pathExists(installation.backupConfig)
	if err != nil {
		return err
	}
	stagedCertsExist, err := pathExists(installation.stagedCerts)
	if err != nil {
		return err
	}
	stagedConfigExists, err := pathExists(installation.stagedConfig)
	if err != nil {
		return err
	}
	finalCertsExist, err := pathExists(installation.finalCerts)
	if err != nil {
		return err
	}
	finalConfigExists, err := pathExists(installation.finalConfig)
	if err != nil {
		return err
	}
	installation.certsBackedUp = installation.certsBackedUp || backupCertsExist
	installation.configBackedUp = installation.configBackedUp || backupConfigExists
	installation.certsInstalled = installation.certsInstalled ||
		(!stagedCertsExist && finalCertsExist && (!installation.hadCerts || backupCertsExist))
	installation.configInstalled = installation.configInstalled ||
		(!stagedConfigExists && finalConfigExists && (!installation.hadConfig || backupConfigExists))
	return installation.rollback()
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync %s: %w", path, err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", path, err)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspect %s: %w", path, err)
}

func firstRoutableInviteHost(addresses []string) string {
	for _, address := range addresses {
		parsed, err := url.Parse(address)
		if err != nil {
			continue
		}
		host := parsed.Hostname()
		if host != "" && host != "0.0.0.0" && host != "::" {
			return host
		}
	}
	return ""
}

func snapshotNodeGlobals() nodeGlobalsSnapshot {
	srvMutex.RLock()
	defer srvMutex.RUnlock()
	return nodeGlobalsSnapshot{
		storage: appStorage,
		logger:  appLogger,
		running: srv != nil && !appStopping && srv.IsReady(),
	}
}

func restorePreviousNode(previous nodeGlobalsSnapshot, wasStopped bool) error {
	srvMutex.Lock()
	appStorage = previous.storage
	appLogger = previous.logger
	srvMutex.Unlock()
	if !previous.running || !wasStopped {
		return nil
	}
	if result := startJoinedNode(previous.storage, true); result != "" {
		return fmt.Errorf("failed to restart previous node after join rollback: %s", ParseBindError(result))
	}
	return nil
}

func runDelayedJoinSync(ctx context.Context, startedServer interface {
	ExecuteSync() error
	LocalServiceDiscover() ([]string, error)
}) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if ctx.Err() != nil {
		return
	}
	_ = startedServer.ExecuteSync()
	if ctx.Err() == nil {
		_, _ = startedServer.LocalServiceDiscover()
	}
}
