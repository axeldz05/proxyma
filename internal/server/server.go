package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/storage"
	"proxyma/internal/utils"
	"sort"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Config     protocol.NodeConfig
	Compute    *compute.ComputeEngine
	Storage    *storage.StorageEngine
	peerClient p2p.PeerClient

	Peers     *PeerRegistry
	Invites   *InviteManager
	Bandwidth *BandwidthTracker
	Relays    *RelayManager

	handler       http.Handler
	httpServer    *http.Server
	downloadQueue chan DownloadJob
	unixListener  net.Listener
	done          chan struct{}
	shutdownOnce  sync.Once

	isSponsor       bool
	checkNATOnce    sync.Once
	serverTLSConfig *tls.Config
	clientTLSConfig *tls.Config
	tlsMutex        sync.RWMutex

	udpConn         *net.UDPConn
	publicUDPAddr   string
	quicMgr         *p2p.QUICManager
	natMapper       *p2p.NATMapper
}

type DownloadJob struct {
	File   protocol.IndexEntry
	Source string
}

func New(cfg protocol.NodeConfig, peerClient p2p.PeerClient) *Server {
	if cfg.Logger == nil {
		panic("server.New: cfg.Logger must not be nil — use protocol.NewLogger to create it")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	s := &Server{
		Config:        cfg,
		peerClient:    peerClient,
		downloadQueue: make(chan DownloadJob, 100),
		done:          make(chan struct{}),
	}

	s.Peers = NewPeerRegistry(cfg.Logger, cfg.ID)
	s.Invites = NewInviteManager(cfg.Logger)
	s.Bandwidth = NewBandwidthTracker()
	s.Relays = NewRelayManager(s)

	if updater, ok := s.peerClient.(interface {
		UpdateSponsorAddress(addr string)
	}); ok {
		updater.UpdateSponsorAddress(cfg.BootstrapNode)
	}

	if setter, ok := s.peerClient.(interface {
		SetNodeID(id string)
	}); ok {
		setter.SetNodeID(cfg.ID)
	}

	if setter, ok := s.peerClient.(interface {
		SetOwnAddress(addr string)
	}); ok {
		setter.SetOwnAddress(cfg.Address)
	}

	s.Compute = compute.NewComputeEngine(cfg.Logger, s.peerClient, cfg.Workers, cfg.ID)
	s.Compute.SetAddress(cfg.Address)
	s.Storage = storage.NewStorageEngine(cfg.Logger, cfg.StoragePath, s.peerClient, cfg.Workers, s.notifyPeers, func(file protocol.IndexEntry, rawSource string) error {
		for peerID, peerAddress := range s.GetPeersCopy() {
			if rawSource == peerAddress {
				s.downloadQueue <- DownloadJob{
					File:   file,
					Source: peerID,
				}
				return nil
			}
		}
		return fmt.Errorf("peer of address %s not found", rawSource)
	})

	// Load persisted peers from DB and populate registry
	if peers, err := s.Storage.LoadPeers(); err == nil {
		for peerID, record := range peers {
			s.Peers.AddPeer(peerID, record)
			s.Peers.SetPeerOffline(peerID, fmt.Errorf("not contacted yet"))
			if updater, ok := s.peerClient.(interface {
				UpdatePeerRoute(peerID string, record protocol.AddressRecord)
			}); ok {
				updater.UpdatePeerRoute(peerID, record)
			}
		}
	} else {
		cfg.Logger.Error("Failed to load persisted peers", "error", err)
	}

	// Configure compute callbacks
	s.Compute.SetServiceFinder(s.RequestServiceToCluster)
	s.Compute.SetTaskDispatcher(s.DispatchTask)
	s.Compute.SetVFSBlobResolver(func(ctx context.Context, requesterNodeID, hash string) (string, error) {
		hasLocal, _ := s.Storage.HasPhysicalBlob(hash)
		if !hasLocal {
			if requesterNodeID != "" && requesterNodeID != s.Config.ID {
				dlCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				body, err := s.peerClient.DownloadBlob(dlCtx, requesterNodeID, hash)
				if err != nil {
					return "", fmt.Errorf("failed to download VFS blob %s from %s: %w", hash, requesterNodeID, err)
				}
				defer func() { _ = body.Close() }()
				_, _, err = s.Storage.SavePhysicalBlob(body)
				if err != nil {
					return "", fmt.Errorf("failed to store downloaded VFS blob: %w", err)
				}
			}
		}
		return s.Storage.GetBlobPath(hash), nil
	})
	s.Compute.SetVFSBlobStager(func(pathStr string) (string, int64, error) {
		if _, err := os.Stat(pathStr); err != nil {
			return "", 0, err
		}
		f, err := os.Open(pathStr)
		if err != nil {
			return "", 0, err
		}
		defer func() { _ = f.Close() }()
		hash, size, err := s.Storage.SavePhysicalBlob(f)
		if err != nil {
			return "", 0, err
		}
		s.Storage.Upsert(protocol.IndexEntry{
			Name:    filepath.Base(pathStr),
			Hash:    hash,
			Size:    size,
			Version: 1,
		})
		s.Storage.SetSubscription(filepath.Base(pathStr), true)
		return hash, size, nil
	})

	// Load persisted pipeline schemas
	if schemas, err := s.Storage.LoadPipelineSchemas(); err == nil {
		for _, schema := range schemas {
			s.Compute.RegisterPipeline(schema)
		}
	} else {
		cfg.Logger.Error("Failed to load persisted pipeline schemas", "error", err)
	}

	s.handler = s.MountHandlers()

	for range cfg.Workers {
		go s.downloadWorker()
	}

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ticker.C:
				s.Invites.Sweep()
				s.Storage.CleanupTempFiles()
			}
		}
	}()
	s.Storage.CleanupTempFiles()
	return s
}

func (s *Server) ListenAndServe(serverTLS *tls.Config) error {
	go s.listenUnixSocket()

	mux := s.wrapWithBandwidthCounting(s.handler)
	rawAddr := s.Config.Address
	rawAddr = strings.TrimPrefix(rawAddr, "https://")
	rawAddr = strings.TrimPrefix(rawAddr, "http://")
	_, port, err := net.SplitHostPort(rawAddr)
	var addr string
	if err == nil {
		addr = "0.0.0.0:" + port
	} else {
		extractedPort := utils.ExtractPort(s.Config.Address)
		if extractedPort == "" {
			extractedPort = "8080"
		}
		addr = "0.0.0.0:" + extractedPort
	}

	hs := &http.Server{
		Addr:      addr,
		Handler:   mux,
		TLSConfig: serverTLS,
		ErrorLog:  log.New(&tlsErrorWriter{server: s}, "", 0),
	}

	s.httpServer = hs
	s.Config.Logger.Info("Starting secure P2P node", "address", addr)

	// Run NAT auto-detection asynchronously after a brief delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.CheckNAT()
	}()

	return hs.ListenAndServeTLS("", "")
}

func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.shutdownOnce.Do(func() {
		s.Config.Logger.Info("Initiating shutdown...")
		close(s.done)
		s.announceOffline(ctx)
		if s.natMapper != nil {
			s.natMapper.Stop()
		}
		if s.httpServer != nil {
			if hsErr := s.httpServer.Shutdown(ctx); hsErr != nil {
				s.Config.Logger.Error("HTTP server shutdown failed", "error", hsErr)
				err = hsErr
				return
			}
		}
		s.Config.Logger.Info("HTTP server stopped accepting connections.")

		if s.Compute != nil {
			s.Compute.Close()
			s.Config.Logger.Info("Compute Engine closed.")
		}

		if s.quicMgr != nil {
			s.quicMgr.Close()
			s.Config.Logger.Info("QUIC Manager closed.")
		}

		if s.unixListener != nil {
			_ = s.unixListener.Close()
		}

		if s.Storage != nil {
			_ = s.Storage.Close()
			s.Config.Logger.Info("Storage Engine closed.")
		}

		s.Config.Logger.Info("Node shutdown complete.")
	})
	return err
}

func (s *Server) listenUnixSocket() {
	sockPath := filepath.Join(s.Config.StoragePath, "proxyma.sock")
	_ = os.Remove(sockPath) // clean up old socket if it exists
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		s.Config.Logger.Error("Failed to listen on unix socket", "error", err)
		return
	}
	s.unixListener = l
	s.Config.Logger.Info("Listening for local commands on unix socket", "path", sockPath)

	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go s.handleUnixConnection(conn)
	}
}

func (s *Server) handleUnixConnection(c net.Conn) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, 1)
	_, err := c.Read(buf)
	if err != nil {
		return
	}

	// Legacy 1-byte command compat
	if buf[0] == 1 {
		s.Config.Logger.Info("Sync triggered via legacy unix socket command")
		if s.Config.BootstrapNode != "" {
			_ = s.AnnouncePresence(s.Config.BootstrapNode)
		}
		err = s.ExecuteSync()
		if err != nil {
			s.Config.Logger.Error("Sync via legacy unix socket failed", "error", err)
			_, _ = c.Write([]byte{0})
		} else {
			_, _ = c.Write([]byte{1})
		}
		return
	}

	// JSON Request
	if buf[0] == '{' {
		reader := io.MultiReader(bytes.NewReader(buf), c)
		var req protocol.UnixRequest
		if err := json.NewDecoder(reader).Decode(&req); err != nil {
			respBytes, _ := json.Marshal(protocol.UnixResponse{Success: false, Error: "invalid JSON request: " + err.Error()})
			_, _ = c.Write(respBytes)
			return
		}

		var respData any
		var actionErr error

		switch req.Action {
		case "sync":
			if s.Config.BootstrapNode != "" {
				_ = s.AnnouncePresence(s.Config.BootstrapNode)
			}
			actionErr = s.ExecuteSync()

		case "vfs_list":
			respData = s.LocalVFSList()

		case "vfs_upload":
			filePath := req.Args["path"]
			fileName := req.Args["name"]
			if filePath == "" || fileName == "" {
				actionErr = fmt.Errorf("missing path or name parameter")
				break
			}
			f, err := os.Open(filePath)
			if err != nil {
				actionErr = fmt.Errorf("failed to open file: %w", err)
				break
			}
			actionErr = s.Storage.SaveLocalFile(fileName, f)
			_ = f.Close()

		case "vfs_subscribe":
			fileName := req.Args["name"]
			if fileName == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			s.Storage.SetSubscription(fileName, true)
			go func() {
				if s.Config.BootstrapNode != "" {
					_ = s.AnnouncePresence(s.Config.BootstrapNode)
				}
				_ = s.ExecuteSync()
			}()

		case "vfs_unsubscribe":
			fileName := req.Args["name"]
			if fileName == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			s.Storage.SetSubscription(fileName, false)

		case "vfs_delete":
			fileName := req.Args["name"]
			if fileName == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			actionErr = s.Storage.DeleteLocalFile(fileName)

		case "vfs_purge":
			fileName := req.Args["name"]
			if fileName == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			actionErr = s.Storage.DeleteLocalCache(fileName)

		case "vfs_fetch":
			fileName := req.Args["name"]
			if fileName == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			actionErr = s.FetchFileOnDemand(fileName)

		case "service_discover":
			respData, actionErr = s.LocalServiceDiscover()

		case "service_detail":
			name := req.Args["name"]
			if name == "" {
				actionErr = fmt.Errorf("missing name parameter")
				break
			}
			schema, exists := s.Compute.GetService(name)
			if exists {
				respData = schema
				break
			}
			_, _, schema2, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
			if err != nil {
				actionErr = err
				break
			}
			respData = schema2

		case "service_add":
			respData, actionErr = s.LocalServiceAdd(
				req.Args["name"],
				req.Args["type"],
				req.Args["exec"],
				req.Args["desc"],
				req.Args["param"],
				req.Args["no-required"],
				req.Args["schema-file"],
			)

		case "service_remove":
			respData, actionErr = s.LocalServiceRemove(req.Args["name"])

		case "service_run":
			respData, actionErr = s.LocalServiceRun(req.Args["service"], req.Args["payload"])

		case "service_run_file":
			respData, actionErr = s.LocalServiceRunFile(req.Args["service"], req.Args["input"], req.Args["output"], req.Args["param"])

		case "service_status":
			taskID := req.Args["task_id"]
			if taskID == "" {
				respData = s.Compute.GetAllTaskStatuses()
			} else {
				r, ok := s.Compute.GetTaskResponse(taskID)
				if !ok {
					actionErr = fmt.Errorf("task not found")
					break
				}
				respData = r
			}

		case "invite_generate":
			respData, actionErr = s.LocalInviteGenerate(15)

		case "logs":
			protocol.LogBufferMu.RLock()
			logsCopy := make([]protocol.LogRecord, len(protocol.LogBuffer))
			copy(logsCopy, protocol.LogBuffer)
			protocol.LogBufferMu.RUnlock()
			respData = logsCopy

		case "bandwidth":
			respData = s.LocalBandwidthStats()

		case "peers":
			respData = s.LocalPeersList()

		case "pipeline_add":
			actionErr = s.LocalPipelineAdd(req.Args["schema"])

		case "pipeline_validate":
			actionErr = s.LocalPipelineValidate(req.Args["schema"])

		case "pipeline_remove":
			actionErr = s.LocalPipelineRemove(req.Args["id"])

		case "pipeline_list":
			respData = s.LocalPipelineList()

		case "pipeline_get":
			respData, actionErr = s.LocalPipelineGet(req.Args["id"])

		case "pipeline_clone":
			respData, actionErr = s.LocalPipelineClone(req.Args["id"], req.Args["new_id"], req.Args["target_node"])

		default:
			actionErr = fmt.Errorf("unknown action: %s", req.Action)
		}

		var unixResp protocol.UnixResponse
		if actionErr != nil {
			unixResp = protocol.UnixResponse{Success: false, Error: actionErr.Error()}
		} else {
			var raw json.RawMessage
			if respData != nil {
				raw, _ = json.Marshal(respData)
			}
			unixResp = protocol.UnixResponse{Success: true, Data: raw}
		}

		respBytes, _ := json.Marshal(unixResp)
		_, _ = c.Write(respBytes)
	}
}

type LocalService struct {
	Type   string                 `json:"type"`
	Exec   string                 `json:"exec,omitempty"`
	Schema protocol.ServiceSchema `json:"schema"`
}

func (s *Server) LoadLocalServices() {
	s.Compute.ClearServices()
	servicesFile := filepath.Join(s.Config.StoragePath, "services.json")
	data, err := os.ReadFile(servicesFile)
	if err != nil {
		if os.IsNotExist(err) {
			s.Config.Logger.Info("No services.json found, skipping local service registration")
			return
		}
		s.Config.Logger.Error("Failed to read services.json", "error", err)
		return
	}

	var services map[string]LocalService
	if err := json.Unmarshal(data, &services); err != nil {
		s.Config.Logger.Error("Failed to unmarshal services.json", "error", err)
		return
	}

	for name, svc := range services {
		var handler compute.ServiceHandler
		switch svc.Type {
		case "script", "exec":
			handler = compute.BuildScriptHandler(svc.Exec)
		case "grpc":
			handler = compute.BuildGRPCHandler(svc.Exec, 10*time.Second)
		default:
			s.Config.Logger.Warn("Unknown service type", "type", svc.Type, "service", name)
			continue
		}

		if err := s.Compute.RegisterNewService(svc.Schema, handler); err != nil {
			s.Config.Logger.Error("Failed to register local service", "service", name, "error", err)
		} else {
			s.Config.Logger.Info("Local service registered", "service", name, "type", svc.Type)
		}
	}
}

// Delegated methods for backward compatibility & Continuous Granularity.

func (s *Server) SetPeerOnline(peerID string, online bool) {
	s.Peers.SetPeerOnline(peerID, online)
}

func (s *Server) SetPeerOffline(peerID string, err error) {
	s.Peers.SetPeerOffline(peerID, err)
}

func (s *Server) IsPeerOnline(peerID string) bool {
	return s.Peers.IsPeerOnline(peerID)
}

func (s *Server) RemovePeer(peerID string) {
	s.Peers.RemovePeer(peerID)
	if s.Storage != nil {
		_ = s.Storage.DeletePeer(peerID)
	}
	if updater, ok := s.peerClient.(interface {
		RemovePeerRoute(peerID string)
	}); ok {
		updater.RemovePeerRoute(peerID)
	}
}

func (s *Server) announceOffline(ctx context.Context) {
	peers := s.GetPeersCopy()
	payload := map[string]string{"id": s.Config.ID}
	for peerID := range peers {
		_ = s.peerClient.Offline(ctx, peerID, payload)
	}
}

func (s *Server) GetClusterServices(peerID string) map[string]protocol.ServiceSchema {
	return s.Peers.GetClusterServices(peerID)
}

func (s *Server) SetAddress(addr string) {
	s.Config.Address = addr
	s.Compute.SetAddress(addr)
}

func (s *Server) AddPeer(peerID string, addressRecord protocol.AddressRecord) {
	if s.Peers.AddPeer(peerID, addressRecord) {
		if s.Storage != nil {
			if err := s.Storage.SavePeer(peerID, addressRecord); err != nil {
				s.Config.Logger.Error("Failed to save peer to DB", "peerID", peerID, "error", err)
			}
		}
		if updater, ok := s.peerClient.(interface {
			UpdatePeerRoute(peerID string, record protocol.AddressRecord)
		}); ok {
			updater.UpdatePeerRoute(peerID, addressRecord)
		}
		go func(targetPeer string) {
			for _, schema := range s.Compute.ListPipelines() {
				s.NotifySchemaToPeer(targetPeer, schema, "add")
			}
		}(peerID)
	}
}

func (s *Server) notifyPeers(fileInfo protocol.IndexEntry) {
	for peerID := range s.GetPeersCopy() {
		payload := protocol.PeerNotification{
			File:   fileInfo,
			Source: s.Config.Address,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := s.peerClient.Notify(ctx, peerID, payload)
		if err != nil {
			s.Config.Logger.Debug("Unreachable peer for real-time notification", "peerID", peerID, "error", err)
			s.SetPeerOffline(peerID, err)
		} else {
			s.SetPeerOnline(peerID, true)
		}
	}
}

func (s *Server) RequestServiceToCluster(query protocol.DiscoveryQuery) (string, string, protocol.ServiceSchema, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var bids []protocol.ServiceBid
	var mu sync.Mutex
	var wg sync.WaitGroup

	peers := s.GetPeersCopy()
	for peerID := range peers {
		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()
			bid, err := s.peerClient.FetchServiceBid(ctx, peerID, query)
			if err != nil {
				s.Config.Logger.Error("FetchServiceBid failed", "peerID", peerID, "err", err)
				s.SetPeerOffline(peerID, err)
			} else {
				s.SetPeerOnline(peerID, true)
			}
			if err != nil || !bid.CanAccept {
				return
			}
			mu.Lock()
			bids = append(bids, bid)
			mu.Unlock()
		}(peerID)
	}

	wg.Wait()

	if len(bids) == 0 {
		return "", "", protocol.ServiceSchema{}, fmt.Errorf("no nodes available for service '%s'", query.Service)
	}

	bestBid := bids[0]
	if query.SortStrategy == protocol.StrategyFastest {
		for _, bid := range bids {
			if bid.EstimatedMillis < bestBid.EstimatedMillis {
				bestBid = bid
			}
		}
	}

	return bestBid.NodeID, bestBid.NodeAddr, bestBid.Schema, nil
}

func (s *Server) DispatchTask(targetPeerID string, req protocol.TaskRequest) error {
	if req.Payload != nil {
		for k, v := range req.Payload {
			if pathStr, ok := v.(string); ok && pathStr != "" && !strings.HasPrefix(pathStr, "vfs://") {
				if fi, err := os.Stat(pathStr); err == nil && !fi.IsDir() {
					f, err := os.Open(pathStr)
					if err == nil {
						hash, _, err := s.Storage.SavePhysicalBlob(f)
						_ = f.Close()
						if err == nil {
							s.Storage.Upsert(protocol.IndexEntry{
								Name:    filepath.Base(pathStr),
								Hash:    hash,
								Size:    fi.Size(),
								Version: 1,
							})
							req.Payload[k] = "vfs://" + hash
						}
					}
				}
			}
		}
	}

	s.Compute.RegisterOutgoingTask(req)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.peerClient.SubmitTask(ctx, targetPeerID, req)
	if err != nil {
		s.Compute.MarkTaskAsFailed(req, err.Error())
		s.SetPeerOffline(targetPeerID, err)
		return err
	}
	s.SetPeerOnline(targetPeerID, true)
	return nil
}

func (s *Server) GetPeersCopy() map[string]string {
	return s.Peers.GetPeersCopy()
}

func (s *Server) GetSponsorPeers() map[string]string {
	return s.Peers.GetSponsorPeers()
}

func (s *Server) ensureQUICSession(peerID string) {
	if s.quicMgr == nil {
		return
	}
	record, ok := s.Peers.GetPeerRecord(peerID)
	if !ok {
		return
	}
	hasQuic := false
	for _, addr := range record.Addresses {
		if strings.HasPrefix(addr, "quic://") {
			hasQuic = true
			break
		}
	}
	if !hasQuic {
		return
	}
	if _, sessionExists := s.quicMgr.GetSession(peerID); sessionExists {
		return
	}

	if updater, ok := s.peerClient.(interface {
		UpdatePeerRoute(peerID string, record protocol.AddressRecord)
	}); ok {
		updater.UpdatePeerRoute(peerID, record)
	}

	// Wait for the session to be established
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer waitCancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			return
		case <-ticker.C:
			if _, sessionExists := s.quicMgr.GetSession(peerID); sessionExists {
				return
			}
		}
	}
}

func (s *Server) ExecuteSync() error {
	for peerID := range s.GetPeersCopy() {
		s.ensureQUICSession(peerID)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ctx = context.WithValue(ctx, p2p.BypassHolePunchKey{}, true)
		manifest, err := s.peerClient.FetchManifest(ctx, peerID)
		cancel()
		if err != nil {
			s.Config.Logger.Warn("Sync skipped for peer: couldn't fetch manifest", "peer", peerID, "error", err)
			s.SetPeerOffline(peerID, err)
			continue
		}
		s.SetPeerOnline(peerID, true)
		missingFiles := s.Storage.ProcessRemoteManifest(manifest)
		for _, file := range missingFiles {
			s.downloadQueue <- DownloadJob{
				File:   file,
				Source: peerID,
			}
		}
	}
	return nil
}

func (s *Server) AnnouncePresence(sponsorAddress string) error {
	s.CheckNAT()
	addresses := []string{s.Config.Address}
	if s.isSponsor && s.publicUDPAddr != "" {
		host, _, err := net.SplitHostPort(s.publicUDPAddr)
		if err == nil {
			tcpPortStr := utils.ExtractPort(s.Config.Address)
			if s.natMapper != nil {
				if mappedTCP, _ := s.natMapper.GetMappedPorts(); mappedTCP > 0 {
					tcpPortStr = fmt.Sprintf("%d", mappedTCP)
				}
			}
			publicTCPAddr := fmt.Sprintf("https://%s:%s", host, tcpPortStr)
			addresses = append(addresses, publicTCPAddr)
		}
	}
	if s.publicUDPAddr != "" {
		addresses = append(addresses, "quic://"+s.publicUDPAddr)
	}
	payload := protocol.AddPeerRequest{
		ID: s.Config.ID,
		Address: protocol.AddressRecord{
			Addresses: addresses,
			IsSponsor: s.isSponsor,
		},
	}

	announceResp, err := s.peerClient.Announce(sponsorAddress, payload)
	if err != nil {
		s.Config.Logger.Error("Error while announcing from sponsor", "sponsor", sponsorAddress, "error", err)
		return fmt.Errorf("there was an error trying to connect to the cluster: %v", err)
	}
	s.Config.Logger.Info("AnnounceResp received without errors", "resp", announceResp)
	for id, addrRec := range announceResp {
		if id != s.Config.ID {
			s.AddPeer(id, addrRec)
		}
	}
	s.Config.Logger.Info("Successfully synced topology from sponsor", "peers_count", len(announceResp))
	go func() {
		_ = s.ExecuteSync()
	}()
	return nil
}

func (s *Server) CheckNAT() {
	s.checkNATOnce.Do(func() {
		s.determineSponsorAndNATStatus()
	})
}

func (s *Server) downloadWorker() {
	for {
		select {
		case <-s.done:
			return
		case job, ok := <-s.downloadQueue:
			if !ok {
				return
			}
			if job.File.Deleted {
				s.Storage.ProcessRemoteDeletion(job.File)
				continue
			}
			ctxTimeout, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			body, err := s.peerClient.DownloadBlob(ctxTimeout, job.Source, job.File.Hash)
			if err != nil {
				cancel()
				s.Config.Logger.Error("Network error downloading blob", "file", job.File.Name, "error", err)
				continue
			}
			err = s.Storage.StoreRemoteBlob(job.File, body)
			_ = body.Close()
			cancel()
			if err != nil {
				s.Config.Logger.Error("Failed to apply remote blob", "error", err)
			}
		}
	}
}

func (s *Server) AddPendingInvite(secret string, expiration time.Time) {
	s.Invites.Add(secret, expiration)
}

func (s *Server) DiscoverServices(ctx context.Context, peerID string) ([]string, error) {
	return s.peerClient.DiscoverServices(ctx, peerID)
}

func (s *Server) GetPeerRecord(peerID string) (protocol.AddressRecord, bool) {
	return s.Peers.GetPeerRecord(peerID)
}

func (s *Server) GetPeersRecordCopy() map[string]protocol.AddressRecord {
	return s.Peers.GetPeersRecordCopy()
}

func (s *Server) RecordBytesSent(n int64, path string) {
	s.Bandwidth.RecordBytesSent(n, path)
}

func (s *Server) RecordBytesReceived(n int64, path string) {
	s.Bandwidth.RecordBytesReceived(n, path)
}

func (s *Server) GetCurrentBandwidth() (float64, float64) {
	return s.Bandwidth.GetCurrentBandwidth()
}

func (s *Server) GetCategoryBandwidth(category string) (float64, float64) {
	return s.Bandwidth.GetCategoryBandwidth(category)
}

func (s *Server) GetTotalBandwidth() (int64, int64) {
	return s.Bandwidth.GetTotalBandwidth()
}

type countingResponseWriter struct {
	http.ResponseWriter
	bytesWritten int64
	onWrite      func(int)
}

func (w *countingResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	if w.onWrite != nil && n > 0 {
		w.onWrite(n)
	}
	return n, err
}

func (s *Server) wrapWithBandwidthCounting(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = &utils.CountingReadCloser{
				ReadCloser: r.Body,
				OnRead: func(n int) {
					s.RecordBytesReceived(int64(n), r.URL.RequestURI())
				},
			}
		}

		cw := &countingResponseWriter{
			ResponseWriter: w,
			onWrite: func(n int) {
				s.RecordBytesSent(int64(n), r.URL.RequestURI())
			},
		}
		next.ServeHTTP(cw, r)
	})
}

func (s *Server) SetTLSConfigs(serverTLS, clientTLS *tls.Config) {
	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()
	s.serverTLSConfig = serverTLS
	s.clientTLSConfig = clientTLS
}

func (s *Server) ReloadTLSConfig(caPath, certPath, keyPath string) error {
	newServerTLS, newClientTLS, err := p2p.LoadNodeTLS(caPath, certPath, keyPath)
	if err != nil {
		return fmt.Errorf("failed to load rotated TLS certs: %w", err)
	}

	s.tlsMutex.Lock()
	defer s.tlsMutex.Unlock()

	s.Config.Logger.Info("Reloading dynamic TLS configuration across server and client...")

	if s.serverTLSConfig != nil {
		s.serverTLSConfig.Certificates = newServerTLS.Certificates
		s.serverTLSConfig.ClientCAs = newServerTLS.ClientCAs
	}

	if s.clientTLSConfig != nil {
		s.clientTLSConfig.Certificates = newClientTLS.Certificates
		s.clientTLSConfig.RootCAs = newClientTLS.RootCAs
		s.clientTLSConfig.VerifyPeerCertificate = newClientTLS.VerifyPeerCertificate
	}

	return nil
}

func (s *Server) RotateCAAndResignPeers() {
	caKeyPath := strings.Replace(s.Config.CAPath, ".crt", ".key", 1)
	if _, err := os.Stat(caKeyPath); err != nil {
		s.Config.Logger.Debug("We are not the CA authority node, skipping CA rotation")
		return
	}

	s.Config.Logger.Info("Triggering CA Rotation & Peer Re-signing...")

	// 1. Rotate the CA files
	certsDir := filepath.Dir(s.Config.CAPath)
	err := p2p.RotateCA(certsDir)
	if err != nil {
		s.Config.Logger.Error("Failed to rotate CA", "error", err)
		return
	}

	// 2. Re-sign own certificate with the new CA
	ownCertFile := filepath.Join(certsDir, fmt.Sprintf("%s.crt", s.Config.ID))
	ownKeyFile := filepath.Join(certsDir, fmt.Sprintf("%s.key", s.Config.ID))
	err = p2p.IssueNodeCertificate(certsDir, certsDir, s.Config.ID)
	if err != nil {
		s.Config.Logger.Error("Failed to re-sign own certificate", "error", err)
		return
	}

	// 3. Loop over all other registered peers and re-sign their certificates
	peers := s.GetPeersCopy()
	var wg sync.WaitGroup

	for peerID, addr := range peers {
		if peerID == s.Config.ID {
			continue
		}

		cert, hasCert := s.Peers.GetPeerCertificate(peerID)
		if !hasCert {
			s.Config.Logger.Warn("No client certificate cached for peer, cannot re-sign. They must re-join.", "peerID", peerID)
			continue
		}

		wg.Add(1)
		go func(pid, paddr string, pcert *x509.Certificate) {
			defer wg.Done()

			newCertPEM, err := p2p.ReSignPeerCertificate(pcert.PublicKey, pid, s.Config.CAPath, caKeyPath)
			if err != nil {
				s.Config.Logger.Error("Failed to re-sign peer certificate", "peerID", pid, "error", err)
				return
			}

			caCertPEM, err := os.ReadFile(s.Config.CAPath)
			if err != nil {
				return
			}

			rotationPayload := map[string]string{
				"ca_cert":   string(caCertPEM),
				"node_cert": string(newCertPEM),
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err = s.peerClient.RotateTLS(ctx, pid, rotationPayload)
			if err != nil {
				s.Config.Logger.Error("Failed to push rotated TLS certs to peer", "peerID", pid, "error", err)
			} else {
				s.Config.Logger.Info("Successfully pushed rotated TLS certs to peer", "peerID", pid)
			}
		}(peerID, addr, cert)
	}

	wg.Wait()

	// 4. Finally, reload our own TLS config in place
	err = s.ReloadTLSConfig(s.Config.CAPath, ownCertFile, ownKeyFile)
	if err != nil {
		s.Config.Logger.Error("Failed to reload own TLS config after rotation", "error", err)
		return
	}
	s.Config.Logger.Info("CA Rotation completed successfully on Sponsor/CA node.")
}

func (s *Server) LocalVFSList() []protocol.VFSFileStatus {
	snapshot := s.Storage.GetVFSSnapshot()
	list := []protocol.VFSFileStatus{}
	for _, entry := range snapshot {
		if entry.Deleted {
			continue
		}
		hasLocal, _ := s.Storage.HasPhysicalBlob(entry.Hash)
		isSubscribed := s.Storage.IsSubscribed(entry.Name)
		sentSpeed, recvSpeed := s.GetCategoryBandwidth("vfs:" + entry.Hash)

		list = append(list, protocol.VFSFileStatus{
			Name:       entry.Name,
			Version:    entry.Version,
			Size:       entry.Size,
			Hash:       entry.Hash,
			Subscribed: isSubscribed,
			HasLocal:   hasLocal,
			Deleted:    entry.Deleted,
			UpSpeed:    sentSpeed,
			DownSpeed:  recvSpeed,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

func (s *Server) FetchFileOnDemand(name string) error {
	entry, ok := s.Storage.GetFileMeta(name)
	if !ok {
		return fmt.Errorf("file '%s' not found in VFS metadata", name)
	}
	hasLocal, _ := s.Storage.HasPhysicalBlob(entry.Hash)
	if hasLocal {
		return nil
	}

	peersSnapshot := s.GetPeersRecordCopy()
	var downloadErr error
	for peerID, addrRec := range peersSnapshot {
		if peerID == s.Config.ID || len(addrRec.Addresses) == 0 {
			continue
		}
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		body, err := s.peerClient.DownloadBlob(ctxTimeout, peerID, entry.Hash)
		if err != nil {
			cancel()
			downloadErr = err
			continue
		}
		err = s.Storage.StoreRemoteBlob(entry, body)
		_ = body.Close()
		cancel()
		if err == nil {
			s.Config.Logger.Info("Successfully fetched unsubscribed blob on demand into cache", "file", name, "hash", entry.Hash)
			return nil
		}
		downloadErr = err
	}

	if downloadErr != nil {
		return fmt.Errorf("failed to fetch file '%s' from cluster peers: %v", name, downloadErr)
	}
	return fmt.Errorf("no peer holds physical replica for file '%s'", name)
}

func (s *Server) LocalServiceDiscover() ([]string, error) {
	names := make(map[string]bool)
	for _, name := range s.Compute.ListServices() {
		names[name] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peers := s.GetPeersCopy()
	var discoveryErr error
	for peerID := range peers {
		peerSvc, err := s.DiscoverServices(ctx, peerID)
		if err == nil {
			for _, name := range peerSvc {
				names[name] = true
			}
		} else {
			discoveryErr = err
			s.Config.Logger.Warn("Service discovery from cluster peer failed", "peerID", peerID, "error", err)
		}
	}
	var result []string
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	s.Config.Logger.Info("Service discovery scan completed", "peers_scanned", len(peers), "services_found", len(result), "last_err", discoveryErr)
	return result, nil
}

func (s *Server) LocalServiceRun(serviceName string, payloadStr string) (protocol.ServiceTaskResponse, error) {
	var payload map[string]any
	if payloadStr != "" {
		_ = json.Unmarshal([]byte(payloadStr), &payload)
	}

	var targetPeerID string
	var err error

	if _, isPipeline := s.Compute.GetPipeline(serviceName); isPipeline {
		targetPeerID = s.Config.ID
	} else {
		targetPeerID, _, _, err = s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: serviceName})
		if err != nil {
			return protocol.ServiceTaskResponse{}, fmt.Errorf("failed to discover service: %w", err)
		}
	}

	taskID := fmt.Sprintf("task_kt_%d", time.Now().UnixNano())
	taskReq := protocol.TaskRequest{
		TaskID:          taskID,
		Service:         serviceName,
		RequesterNodeID: s.Config.ID,
		ReplyTo:         fmt.Sprintf("https://%s.proxyma.local/services/callback", s.Config.ID),
		Payload:         payload,
	}

	s.Compute.RegisterOutgoingTask(taskReq)

	if targetPeerID == s.Config.ID {
		err = s.Compute.SubmitTask(taskReq)
		if err != nil {
			s.Compute.MarkTaskAsFailed(taskReq, err.Error())
			return protocol.ServiceTaskResponse{}, fmt.Errorf("failed to submit local task: %w", err)
		}
	} else {
		err = s.DispatchTask(targetPeerID, taskReq)
		if err != nil {
			s.Compute.MarkTaskAsFailed(taskReq, err.Error())
			return protocol.ServiceTaskResponse{}, err
		}
	}

	var resp protocol.ServiceTaskResponse
	completed := false
	for i := 0; i < 90; i++ {
		time.Sleep(1 * time.Second)
		r, ok := s.Compute.GetTaskResponse(taskID)
		if ok {
			if r.Status == "completed" || r.Status == "failed" {
				resp = r
				completed = true
				break
			}
		}
	}
	if !completed {
		return protocol.ServiceTaskResponse{}, fmt.Errorf("task timed out on execution")
	}

	if completed && resp.Status == "completed" && resp.Outputs != nil {
		var outputHash string
		var outputName string
		var outputSize int64

		if h, ok := resp.Outputs["output_hash"].(string); ok && h != "" {
			outputHash = h
			outputName, _ = resp.Outputs["output_name"].(string)
			if sz, ok := resp.Outputs["output_size"].(float64); ok {
				outputSize = int64(sz)
			}
		} else {
			for k, v := range resp.Outputs {
				if pathStr, ok := v.(string); ok && strings.HasPrefix(pathStr, "vfs://") {
					outputHash = filepath.Base(strings.TrimPrefix(pathStr, "vfs://"))
					outputName = k
					break
				}
			}
		}

		if outputHash != "" {
			if outputName == "" {
				outputName = outputHash + ".pdf"
			}
			nextVersion := 1
			if existing, exists := s.Storage.GetFileMeta(outputName); exists {
				nextVersion = existing.Version + 1
			}
			outputMeta := protocol.IndexEntry{
				Name:    outputName,
				Hash:    outputHash,
				Size:    outputSize,
				Version: nextVersion,
			}
			s.Storage.Upsert(outputMeta)
			s.Storage.SetSubscription(outputName, true)

			dlCtx, dlCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			body, err := s.peerClient.DownloadBlob(dlCtx, targetPeerID, outputHash)
			if err == nil {
				_ = s.Storage.StoreRemoteBlob(outputMeta, body)
				_ = body.Close()
				localPath := s.Storage.GetBlobPath(outputHash)
				resp.Outputs["result_path"] = localPath
				for k, v := range resp.Outputs {
					if pathStr, ok := v.(string); ok && (strings.HasPrefix(pathStr, "vfs://") || pathStr == outputHash) {
						resp.Outputs[k] = localPath
					}
				}
			} else {
				s.Config.Logger.Error("Failed to auto-download output blob", "hash", outputHash, "error", err)
			}
			dlCancel()
		}
	}

	return resp, nil
}

func (s *Server) LocalServiceRunFile(serviceName, inputPath, outputName, paramStr string) (protocol.ServiceTaskResponse, error) {
	var tempInputName string
	var inputHash string
	var inputSize int64

	if outputName == "" {
		outputName = "output_" + filepath.Base(inputPath)
	}

	if _, err := os.Stat(inputPath); err == nil {
		f, err := os.Open(inputPath)
		if err != nil {
			return protocol.ServiceTaskResponse{}, fmt.Errorf("failed to open local input file: %w", err)
		}
		defer func() { _ = f.Close() }()

		tempInputName = "temp_ocr_" + filepath.Base(inputPath)
		hashVal, sizeVal, err := s.Storage.SaveLocalFileWithoutNotification(tempInputName, f)
		if err != nil {
			return protocol.ServiceTaskResponse{}, fmt.Errorf("failed to save input file: %w", err)
		}
		inputHash = hashVal
		inputSize = sizeVal
	} else {
		meta, ok := s.Storage.GetFileMeta(inputPath)
		if !ok {
			return protocol.ServiceTaskResponse{}, fmt.Errorf("input file not found on disk or VFS: %s", inputPath)
		}
		tempInputName = inputPath
		inputHash = meta.Hash
		inputSize = meta.Size
	}

	payload := make(map[string]any)
	if paramStr != "" {
		_ = json.Unmarshal([]byte(paramStr), &payload)
	}
	payload["input_name"] = tempInputName
	payload["input_hash"] = inputHash
	payload["input_size"] = inputSize
	payload["output_name"] = outputName
	payload["requester_node_id"] = s.Config.ID

	payloadBytes, _ := json.Marshal(payload)
	return s.LocalServiceRun(serviceName, string(payloadBytes))
}

func (s *Server) LocalInviteGenerate(validForMinutes int) (string, error) {
	if validForMinutes <= 0 {
		validForMinutes = 15
	}
	smartToken, secretHex, err := p2p.GenerateSmartToken(s.Config.Address, s.Config.CAPath, s.Config.ID, s.Config.BootstrapNode)
	if err != nil {
		return "", err
	}
	expiration := time.Now().Add(time.Duration(validForMinutes) * time.Minute)
	s.AddPendingInvite(secretHex, expiration)
	return smartToken, nil
}

func (s *Server) LocalBandwidthStats() protocol.BandwidthStats {
	upSpeed, downSpeed := s.GetCurrentBandwidth()
	totalSent, totalRecv := s.GetTotalBandwidth()
	return protocol.BandwidthStats{
		UploadSpeed:   int64(upSpeed),
		DownloadSpeed: int64(downSpeed),
		TotalSent:     totalSent,
		TotalReceived: totalRecv,
	}
}

func (s *Server) LocalPeersList() []protocol.PeerStatus {
	var list []protocol.PeerStatus
	for id, addr := range s.GetPeersCopy() {
		online := s.IsPeerOnline(id)
		var errMsg string
		if !online {
			errMsg = s.Peers.GetPeerError(id)
		}
		list = append(list, protocol.PeerStatus{
			ID:      id,
			Address: addr,
			Online:  online,
			Error:   errMsg,
		})
	}
	return list
}

type tlsErrorWriter struct {
	server *Server
}

func (w *tlsErrorWriter) Write(p []byte) (n int, err error) {
	line := string(p)
	if strings.Contains(line, "TLS handshake error") {
		parts := strings.SplitN(line, "TLS handshake error from ", 2)
		if len(parts) == 2 {
			addrPart := parts[1]
			addrErrParts := strings.SplitN(addrPart, ": ", 2)
			if len(addrErrParts) == 2 {
				hostPort := addrErrParts[0]
				errMsg := addrErrParts[1]

				host, _, err := net.SplitHostPort(hostPort)
				if err != nil {
					host = hostPort
				}

				// Find registered peer matching this IP/Host
				peerID := ""
				peerAddr := ""
				for id, pRecord := range w.server.Peers.GetPeersRecordCopy() {
					for _, a := range pRecord.Addresses {
						if strings.Contains(a, host) {
							peerID = id
							peerAddr = a
							break
						}
					}
					if peerID != "" {
						break
					}
				}

				if peerID != "" {
					w.server.Config.Logger.Error("TLS Handshake error from registered peer",
						"peerID", peerID,
						"peerAddress", peerAddr,
						"remoteAddr", hostPort,
						"error", strings.TrimSpace(errMsg),
					)
					w.server.SetPeerOffline(peerID, fmt.Errorf("TLS handshake failed: %s", strings.TrimSpace(errMsg)))
					return len(p), nil
				} else {
					w.server.Config.Logger.Error("TLS Handshake error from unknown source",
						"remoteAddr", hostPort,
						"error", strings.TrimSpace(errMsg),
					)
					return len(p), nil
				}
			}
		}
	}
	// Fallback to standard logging to avoid suppressing other HTTP server errors
	w.server.Config.Logger.Error("HTTP server error", "message", strings.TrimSpace(line))
	return len(p), nil
}

func (s *Server) LocalServiceAdd(name, serviceType, exec, desc, param, noRequired, schemaFile string) (string, error) {
	if serviceType == "" {
		serviceType = "exec"
	}

	servicesFile := filepath.Join(s.Config.StoragePath, "services.json")
	services := make(map[string]LocalService)

	if data, err := os.ReadFile(servicesFile); err == nil {
		_ = json.Unmarshal(data, &services)
	}

	var localService LocalService
	var serviceName string

	if strings.HasSuffix(name, ".json") || schemaFile != "" {
		fileToRead := name
		if schemaFile != "" {
			fileToRead = schemaFile
		}
		data, err := os.ReadFile(fileToRead)
		if err != nil {
			return "", fmt.Errorf("couldn't read service file: %w", err)
		}
		if schemaFile != "" {
			var schema protocol.ServiceSchema
			if err := json.Unmarshal(data, &schema); err != nil {
				return "", fmt.Errorf("invalid schema file format: %w", err)
			}
			localService.Schema = schema
			serviceName = name
			localService.Schema.Name = serviceName
		} else {
			if err := json.Unmarshal(data, &localService); err != nil {
				return "", fmt.Errorf("invalid file format: %w", err)
			}
			serviceName = localService.Schema.Name
		}
		if serviceName == "" {
			return "", fmt.Errorf("service name is missing in JSON schema")
		}
		if exec != "" {
			localService.Exec = exec
		}
		if serviceType != "exec" && localService.Type == "" {
			localService.Type = serviceType
		}
	} else {
		serviceName = name
		schema := protocol.ServiceSchema{
			Name:        serviceName,
			Description: desc,
			Parameters:  make(map[string]protocol.ServiceParameter),
		}

		noReqMap := make(map[string]bool)
		if noRequired != "" {
			for _, p := range strings.Split(noRequired, ",") {
				noReqMap[strings.TrimSpace(p)] = true
			}
		}

		if param != "" {
			for _, p := range strings.Split(param, ",") {
				parts := strings.Split(p, ":")
				if len(parts) < 2 {
					return "", fmt.Errorf("invalid parameter format '%s'. Use name:type", p)
				}

				paramName := strings.TrimSpace(parts[0])
				paramType := strings.TrimSpace(parts[1])

				isRequired := true
				if strings.HasSuffix(paramName, "?") {
					paramName = strings.TrimSuffix(paramName, "?")
					isRequired = false
				} else if noReqMap[paramName] {
					isRequired = false
				}

				schema.Parameters[paramName] = protocol.ServiceParameter{
					Type:     paramType,
					Required: isRequired,
				}
			}
		}

		localService = LocalService{
			Type:   serviceType,
			Exec:   exec,
			Schema: schema,
		}
	}

	services[serviceName] = localService

	newData, _ := json.MarshalIndent(services, "", "  ")
	if err := os.WriteFile(servicesFile, newData, 0644); err != nil {
		return "", fmt.Errorf("error saving services file: %w", err)
	}

	s.LoadLocalServices()

	return fmt.Sprintf("Service '%s' added successfully.", serviceName), nil
}

func (s *Server) LocalServiceRemove(name string) (string, error) {
	servicesFile := filepath.Join(s.Config.StoragePath, "services.json")
	services := make(map[string]LocalService)

	if data, err := os.ReadFile(servicesFile); err == nil {
		_ = json.Unmarshal(data, &services)
	}

	if _, exists := services[name]; !exists {
		return "", fmt.Errorf("service '%s' not found", name)
	}

	delete(services, name)

	newData, _ := json.MarshalIndent(services, "", "  ")
	if err := os.WriteFile(servicesFile, newData, 0644); err != nil {
		return "", fmt.Errorf("error saving services file: %w", err)
	}

	s.LoadLocalServices()

	return fmt.Sprintf("Service '%s' removed successfully.", name), nil
}

func (s *Server) ValidatePipelineSchema(schema protocol.PipelineSchema) error {
	if schema.ID == "" {
		return fmt.Errorf("pipeline ID cannot be empty")
	}
	if len(schema.Steps) == 0 {
		return fmt.Errorf("pipeline must have at least one step")
	}

	// 1. Verify unique step IDs and collect them
	stepServices := make(map[string]string) // stepID -> serviceName
	stepNodes := make(map[string]string)    // stepID -> targetNodeID
	var stepIDs []string
	for _, step := range schema.Steps {
		if step.ID == "" {
			return fmt.Errorf("step ID cannot be empty")
		}
		if step.Service == "" {
			return fmt.Errorf("step '%s' service name cannot be empty", step.ID)
		}
		if _, exists := stepServices[step.ID]; exists {
			return fmt.Errorf("duplicate step ID found: '%s'. Each step in a pipeline must have a unique ID", step.ID)
		}
		stepServices[step.ID] = step.Service
		stepNodes[step.ID] = step.TargetNodeID
		stepIDs = append(stepIDs, step.ID)
	}

	// Helper to lookup service schema locally or from peers
	getSchema := func(serviceName string) (protocol.ServiceSchema, bool) {
		if sc, ok := s.Compute.GetService(serviceName); ok {
			return sc, true
		}
		if s.Peers != nil {
			if sc, ok := s.Peers.GetServiceSchema(serviceName); ok {
				return sc, true
			}
		}
		return protocol.ServiceSchema{}, false
	}

	// Helper to format available parameters
	formatParams := func(params map[string]protocol.ServiceParameter) string {
		if len(params) == 0 {
			return "none"
		}
		var list []string
		for k, p := range params {
			list = append(list, fmt.Sprintf("%s (%s)", k, p.Type))
		}
		sort.Strings(list)
		return strings.Join(list, ", ")
	}

	// 2. Validate connections
	for _, conn := range schema.Connections {
		fromStr := conn.FromStep
		toStr := conn.ToStep
		toNodeStr := ""
		if nodeID, ok := stepNodes[toStr]; ok && nodeID != "" {
			toNodeStr = fmt.Sprintf(" on node '%s'", nodeID)
		}

		// Validate FromStep
		if conn.FromStep != "$initial" {
			if _, exists := stepServices[conn.FromStep]; !exists {
				return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: source step '%s' is not defined in pipeline steps %v",
					conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.FromStep, stepIDs)
			}
		}

		// Validate ToStep
		toService, exists := stepServices[conn.ToStep]
		if !exists {
			return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: target step '%s' is not defined in pipeline steps %v",
				conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.ToStep, stepIDs)
		}

		// Look up ToStep service schema
		toSchema, toSchemaExists := getSchema(toService)
		if toSchemaExists {
			// Check if ToPort is a valid parameter in the Target Service
			param, hasParam := toSchema.Parameters[conn.ToPort]
			if !hasParam {
				validParams := formatParams(toSchema.Parameters)
				extraNote := ""
				if _, isOutput := toSchema.Outputs[conn.ToPort]; isOutput {
					extraNote = fmt.Sprintf(" (Note: '%s' is defined as an OUTPUT port for service '%s', not an input parameter!)", conn.ToPort, toService)
				}
				return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: port '%s' is not a valid input parameter for step '%s' (running service '%s'%s). Expected input parameters for service '%s': [%s]%s",
					fromStr, conn.FromPort, toStr, conn.ToPort, conn.ToPort, toStr, toService, toNodeStr, toService, validParams, extraNote)
			}

			// If FromStep is not initial, and we can resolve its schema, check type compatibility
			if conn.FromStep != "$initial" {
				fromService := stepServices[conn.FromStep]
				fromNodeStr := ""
				if nodeID, ok := stepNodes[conn.FromStep]; ok && nodeID != "" {
					fromNodeStr = fmt.Sprintf(" on node '%s'", nodeID)
				}
				fromSchema, fromSchemaExists := getSchema(fromService)
				if fromSchemaExists {
					// Check if FromPort is a valid output in the Source Service
					outParam, hasOutParam := fromSchema.Outputs[conn.FromPort]
					if !hasOutParam {
						if len(fromSchema.Outputs) > 0 {
							validOutputs := formatParams(fromSchema.Outputs)
							return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: port '%s' is not a valid output for step '%s' (running service '%s'%s). Available output ports for service '%s': [%s]",
								fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromPort, conn.FromStep, fromService, fromNodeStr, fromService, validOutputs)
						}
					} else {
						// Verify type compatibility
						if outParam.Type != param.Type {
							return fmt.Errorf("type mismatch on connection link [%s].%s ──► [%s].%s: source port '%s' outputs type '%s' (service '%s'%s, step '%s'), but target port '%s' requires type '%s' (service '%s'%s, step '%s')",
								fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromPort, outParam.Type, fromService, fromNodeStr, conn.FromStep, conn.ToPort, param.Type, toService, toNodeStr, conn.ToStep)
						}
					}
				}
			}
		}
	}

	return nil
}

func (s *Server) LocalPipelineValidate(schemaJSON string) error {
	var schema protocol.PipelineSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return fmt.Errorf("invalid pipeline schema JSON: %w", err)
	}

	if err := s.ValidatePipelineSchema(schema); err != nil {
		return fmt.Errorf("pipeline validation failed: %w", err)
	}
	return nil
}

func (s *Server) LocalPipelineAdd(schemaJSON string) error {
	var schema protocol.PipelineSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return fmt.Errorf("invalid pipeline schema JSON: %w", err)
	}

	if err := s.ValidatePipelineSchema(schema); err != nil {
		return fmt.Errorf("pipeline validation failed: %w", err)
	}

	// Save to DB
	if s.Storage != nil {
		if err := s.Storage.SavePipelineSchema(schema); err != nil {
			return fmt.Errorf("failed to save pipeline schema to DB: %w", err)
		}
	}

	// Register in Compute
	s.Compute.RegisterPipeline(schema)

	// Gossip update
	go s.NotifySchema(schema, "add")

	return nil
}

func (s *Server) LocalPipelineRemove(id string) error {
	if id == "" {
		return fmt.Errorf("pipeline ID cannot be empty")
	}

	// Delete from DB
	if s.Storage != nil {
		if err := s.Storage.DeletePipelineSchema(id); err != nil {
			return fmt.Errorf("failed to delete pipeline schema from DB: %w", err)
		}
	}

	// Unregister from Compute
	s.Compute.UnregisterPipeline(id)

	// Gossip update
	go s.NotifySchema(protocol.PipelineSchema{ID: id}, "remove")

	return nil
}

func (s *Server) LocalPipelineList() []protocol.PipelineSchema {
	return s.Compute.ListPipelines()
}

func (s *Server) LocalPipelineGet(id string) (protocol.PipelineSchema, error) {
	if id == "" {
		return protocol.PipelineSchema{}, fmt.Errorf("pipeline ID cannot be empty")
	}
	if schema, ok := s.Compute.GetPipeline(id); ok {
		return schema, nil
	}
	return protocol.PipelineSchema{}, fmt.Errorf("pipeline schema '%s' not found in cluster", id)
}

func (s *Server) LocalPipelineClone(id string, newID string, targetNodeID string) (protocol.PipelineSchema, error) {
	schema, err := s.LocalPipelineGet(id)
	if err != nil {
		return protocol.PipelineSchema{}, err
	}
	if newID != "" {
		schema.ID = newID
	} else {
		schema.ID = schema.ID + "-custom"
	}
	if targetNodeID == "$local" || targetNodeID == "local" {
		targetNodeID = s.Config.ID
	}
	if targetNodeID != "" {
		for i := range schema.Steps {
			schema.Steps[i].TargetNodeID = targetNodeID
		}
	}
	return schema, nil
}

func (s *Server) NotifySchemaToPeer(peerID string, schema protocol.PipelineSchema, action string) {
	payload := protocol.PipelineNotification{
		Schema: schema,
		Action: action,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.peerClient.NotifyPipelineSchema(ctx, peerID, payload)
	if err != nil {
		s.Config.Logger.Debug("Failed to notify peer about schema update", "peerID", peerID, "pipelineID", schema.ID, "error", err)
	}
}

func (s *Server) NotifySchema(schema protocol.PipelineSchema, action string) {
	for peerID := range s.GetPeersCopy() {
		s.NotifySchemaToPeer(peerID, schema, action)
	}
}
