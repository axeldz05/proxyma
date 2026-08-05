package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"sort"
	"strings"
	"sync"
	"time"
)

func (s *Server) LoadLocalServices() {
	s.Compute.ClearServices()
	services, err := compute.LoadServicesMap(s.Config.StoragePath)
	if err != nil {
		s.Config.Logger.Error("Failed to load services.json", "error", err)
		return
	}
	if len(services) == 0 {
		if _, statErr := os.Stat(compute.ServicesFilePath(s.Config.StoragePath)); os.IsNotExist(statErr) {
			s.Config.Logger.Info("No services.json found, skipping local service registration")
		}
		return
	}

	for name, svc := range services {
		handler, err := compute.BuildHandler(svc.Type, svc.Exec)
		if err != nil {
			s.Config.Logger.Warn("Unknown service type", "type", svc.Type, "service", name, "error", err)
			continue
		}

		if svc.Schema.Type == "" {
			svc.Schema.Type = svc.Type.Normalize()
		} else {
			svc.Schema.Type = svc.Schema.Type.Normalize()
		}

		if err := s.Compute.RegisterNewService(svc.Schema, handler); err != nil {
			s.Config.Logger.Error("Failed to register local service", "service", name, "error", err)
		} else {
			s.Config.Logger.Info("Local service registered", "service", name, "type", svc.Type)
		}
	}
}

func (s *Server) LocalServiceDiscover() ([]string, error) {
	names := make(map[string]bool)
	for _, name := range s.Compute.ListServices() {
		names[name] = true
	}
	var discoveryErr error
	var mu sync.Mutex
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCDiscover, Parallel: true}, func(ctx context.Context, peerID string) error {
		peerSvc, err := s.DiscoverServices(ctx, peerID)
		if err != nil {
			mu.Lock()
			discoveryErr = err
			mu.Unlock()
			s.Config.Logger.Warn("Service discovery from cluster peer failed", "peerID", peerID, "error", err)
			return err
		}
		mu.Lock()
		for _, name := range peerSvc {
			names[name] = true
		}
		mu.Unlock()
		return nil
	})
	var result []string
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	s.Config.Logger.Info("Service discovery scan completed", "peers_scanned", len(s.GetPeersCopy()), "services_found", len(result), "last_err", discoveryErr)
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

			if targetPeerID == "" || targetPeerID == s.Config.ID {
				var rawLocalPath string
				if p, ok := resp.Outputs["output_path"].(string); ok && !strings.HasPrefix(p, "vfs://") {
					rawLocalPath = p
				}
				if rawLocalPath != "" {
					if _, err := os.Stat(rawLocalPath); err == nil {
						if f, err := os.Open(rawLocalPath); err == nil {
							_ = s.Storage.SaveLocalFile(outputName, f)
							_ = f.Close()
						}
					}
				}
			} else {
				dlCtx, dlCancel := context.WithTimeout(context.Background(), PeerRPCBlobLong)
				err := s.fetchBlobFromPeer(dlCtx, targetPeerID, outputMeta)
				dlCancel()
				if err == nil {
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
			}
		}
	}

	return resp, nil
}

func (s *Server) LocalServiceStreamRun(serviceName string, payloadStr string, chunkCallback func(chunk map[string]any)) error {
	var payload map[string]any
	if payloadStr != "" {
		_ = json.Unmarshal([]byte(payloadStr), &payload)
	}
	if payload == nil {
		payload = make(map[string]any)
	}

	handler, exists := s.Compute.GetHandler(serviceName)
	if !exists {
		targetPeerID, _, _, err := s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: serviceName})
		if err != nil || targetPeerID == "" {
			return fmt.Errorf("streaming service '%s' is not registered on this node or cluster: %v", serviceName, err)
		}
		if targetPeerID == s.Config.ID {
			return fmt.Errorf("streaming service '%s' is not registered on this node", serviceName)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		streamBody, err := s.peerClient.StreamService(ctx, targetPeerID, serviceName, payload)
		if err != nil {
			s.Config.Logger.Error("StreamService from remote peer failed", "peerID", targetPeerID, "service", serviceName, "error", err)
			return fmt.Errorf("failed to stream service from remote peer '%s': %w", targetPeerID, err)
		}
		defer func() { _ = streamBody.Close() }()

		scanner := bufio.NewScanner(streamBody)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var chunk map[string]any
			if err := json.Unmarshal([]byte(line), &chunk); err == nil && chunkCallback != nil {
				chunkCallback(chunk)
			}
		}
		return scanner.Err()
	}

	in := make(chan map[string]any, 1)
	out := make(chan map[string]any, 10)
	errChan := make(chan error, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	go func() {
		errChan <- handler.ExecuteStream(ctx, in, out)
	}()

	in <- payload
	close(in)

	for chunk := range out {
		if chunkCallback != nil {
			chunkCallback(chunk)
		}
	}

	if err := <-errChan; err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	return nil
}

func (s *Server) LocalServiceAdd(name, serviceType, exec, desc, param, noRequired, schemaFile string) (string, error) {
	serviceName, localService, err := compute.BuildLocalServiceFromArgs(name, serviceType, exec, desc, param, noRequired, schemaFile)
	if err != nil {
		return "", err
	}
	if err := compute.UpsertLocalService(s.Config.StoragePath, serviceName, localService); err != nil {
		return "", fmt.Errorf("error saving services file: %w", err)
	}
	s.LoadLocalServices()
	return fmt.Sprintf("Service '%s' added successfully.", serviceName), nil
}

func (s *Server) LocalServiceRemove(name string) (string, error) {
	if err := compute.DeleteLocalService(s.Config.StoragePath, name); err != nil {
		return "", err
	}
	s.LoadLocalServices()
	return fmt.Sprintf("Service '%s' removed successfully.", name), nil
}
