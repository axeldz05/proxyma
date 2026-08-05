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
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"sort"
	"strings"
	"time"
)

type LocalService struct {
	Type   protocol.ServiceType   `json:"type"`
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
		case protocol.ServiceTypeScript, protocol.ServiceTypeExec:
			handler = compute.BuildScriptHandler(svc.Exec)
		case protocol.ServiceTypeGRPC:
			handler = compute.BuildGRPCHandler(svc.Exec, 10*time.Second)
		case protocol.ServiceTypeGRPCBidi, protocol.ServiceTypeBidiGRPC, protocol.ServiceTypeBidi, protocol.ServiceTypeBidiStream:
			if strings.HasPrefix(svc.Exec, "http://") || strings.HasPrefix(svc.Exec, "https://") {
				handler = compute.BuildGRPCBidiHandler(svc.Exec, 30*time.Second)
			} else {
				handler = compute.BuildScriptHandler(svc.Exec)
			}
		default:
			s.Config.Logger.Warn("Unknown service type", "type", svc.Type, "service", name)
			continue
		}

		if svc.Schema.Type == "" {
			svc.Schema.Type = svc.Type
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
		if serviceType != string(protocol.ServiceTypeExec) && localService.Type == "" {
			localService.Type = protocol.ServiceType(serviceType)
		}
	} else {
		serviceName = name
		schema := protocol.ServiceSchema{
			Name:        serviceName,
			Type:        protocol.ServiceType(serviceType),
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
			Type:   protocol.ServiceType(serviceType),
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

	stepServices := make(map[string]string)
	stepNodes := make(map[string]string)
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

	for _, conn := range schema.Connections {
		fromStr := conn.FromStep
		toStr := conn.ToStep
		toNodeStr := ""
		if nodeID, ok := stepNodes[toStr]; ok && nodeID != "" {
			toNodeStr = fmt.Sprintf(" on node '%s'", nodeID)
		}

		if conn.FromStep != "$initial" {
			if _, exists := stepServices[conn.FromStep]; !exists {
				return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: source step '%s' is not defined in pipeline steps %v",
					conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.FromStep, stepIDs)
			}
		}

		toService, exists := stepServices[conn.ToStep]
		if !exists {
			return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: target step '%s' is not defined in pipeline steps %v",
				conn.FromStep, conn.FromPort, conn.ToStep, conn.ToPort, conn.ToStep, stepIDs)
		}

		toSchema, toSchemaExists := getSchema(toService)
		if toSchemaExists {
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

			if conn.FromStep != "$initial" {
				fromService := stepServices[conn.FromStep]
				fromNodeStr := ""
				if nodeID, ok := stepNodes[conn.FromStep]; ok && nodeID != "" {
					fromNodeStr = fmt.Sprintf(" on node '%s'", nodeID)
				}
				fromSchema, fromSchemaExists := getSchema(fromService)
				if fromSchemaExists {
					outParam, hasOutParam := fromSchema.Outputs[conn.FromPort]
					if !hasOutParam {
						if len(fromSchema.Outputs) > 0 {
							validOutputs := formatParams(fromSchema.Outputs)
							return fmt.Errorf("invalid connection link [%s].%s ──► [%s].%s: port '%s' is not a valid output for step '%s' (running service '%s'%s). Available output ports for service '%s': [%s]",
								fromStr, conn.FromPort, toStr, conn.ToPort, conn.FromPort, conn.FromStep, fromService, fromNodeStr, fromService, validOutputs)
						}
					} else {
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

	if s.Storage != nil {
		if err := s.Storage.SavePipelineSchema(schema); err != nil {
			return fmt.Errorf("failed to save pipeline schema to DB: %w", err)
		}
	}

	s.Compute.RegisterPipeline(schema)
	go s.NotifySchema(schema, "add")
	return nil
}

func (s *Server) LocalPipelineRemove(id string) error {
	if id == "" {
		return fmt.Errorf("pipeline ID cannot be empty")
	}

	if s.Storage != nil {
		if err := s.Storage.DeletePipelineSchema(id); err != nil {
			return fmt.Errorf("failed to delete pipeline schema from DB: %w", err)
		}
	}

	s.Compute.UnregisterPipeline(id)
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
