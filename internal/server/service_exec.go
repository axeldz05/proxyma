package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"proxyma/internal/p2p"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"strings"
	"time"
)

func parseServicePayload(payloadStr string) (map[string]any, error) {
	if strings.TrimSpace(payloadStr) == "" {
		return make(map[string]any), nil
	}

	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(payloadStr))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid service payload: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("invalid service payload: expected a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid service payload: multiple JSON values")
		}
		return nil, fmt.Errorf("invalid service payload: %w", err)
	}
	return payload, nil
}

func (s *Server) contextWithServerLifetime(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	combined, cancel := context.WithCancel(ctx)
	lifetime := s.lifetimeCtx
	if lifetime == nil {
		return combined, cancel
	}
	if lifetime.Err() != nil {
		cancel()
		return combined, cancel
	}
	stopLifetime := context.AfterFunc(lifetime, cancel)
	return combined, func() {
		stopLifetime()
		cancel()
	}
}

func (s *Server) waitTaskResponse(ctx context.Context, taskID string, timeout time.Duration) (protocol.ServiceTaskResponse, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		r, ok := s.Compute.GetTaskResponse(taskID)
		if ok {
			if r.Status == protocol.TaskStatusIngesting {
				r.Status = "completed"
				return r, nil
			}
			if r.Status == "completed" || r.Status == "failed" {
				return r, nil
			}
		}
		select {
		case <-ctx.Done():
			return protocol.ServiceTaskResponse{}, ctx.Err()
		case <-timer.C:
			return protocol.ServiceTaskResponse{}, fmt.Errorf("task timed out on execution")
		case <-ticker.C:
		}
	}
}

func (s *Server) failTaskOutputIngest(resp *protocol.ServiceTaskResponse, err error) {
	resp.Status = "failed"
	resp.Error = err.Error()
	if resp.Outputs == nil {
		resp.Outputs = make(map[string]any)
	}
	resp.Outputs["error"] = resp.Error
	s.Compute.RecordTaskResponse(*resp)
}

func (s *Server) ingestTaskOutputsContext(
	ctx context.Context,
	resp *protocol.ServiceTaskResponse,
	targetPeerID string,
) {
	if resp.Status != "completed" {
		return
	}
	if resp.Outputs == nil {
		s.Compute.RecordTaskResponse(*resp)
		return
	}
	outputHash, outputName, outputSize := protocol.OutputHashFromOutputs(resp.Outputs)
	if outputHash == "" {
		s.Compute.RecordTaskResponse(*resp)
		return
	}
	if outputName == "" {
		outputName = outputHash + ".pdf"
	}
	outputMeta, err := s.Storage.UpsertAndSubscribe(protocol.IndexEntry{
		Name: outputName,
		Hash: outputHash,
		Size: outputSize,
	}, false)
	if err != nil {
		s.Config.Logger.Error("Failed to upsert task output metadata", "name", outputName, "error", err)
		s.failTaskOutputIngest(resp, fmt.Errorf("output metadata ingest failed: %w", err))
		return
	}

	producerNodeID := resp.ProducerNodeID
	if producerNodeID == "" {
		producerNodeID = targetPeerID
	}
	if producerNodeID == "" || producerNodeID == s.Config.ID {
		rawLocalPath := protocol.ResultLocalPath(resp.Outputs)
		if rawLocalPath != "" {
			f, err := os.Open(rawLocalPath)
			if err != nil {
				s.failTaskOutputIngest(resp, fmt.Errorf("open local output blob: %w", err))
				return
			}
			saveErr := s.Storage.SaveLocalFile(outputName, f)
			closeErr := f.Close()
			if saveErr != nil {
				s.failTaskOutputIngest(resp, fmt.Errorf("save local output blob: %w", saveErr))
				return
			}
			if closeErr != nil {
				s.failTaskOutputIngest(resp, fmt.Errorf("close local output blob: %w", closeErr))
				return
			}
		} else {
			hasLocal, err := s.Storage.HasPhysicalBlob(outputHash)
			if err != nil {
				s.failTaskOutputIngest(resp, fmt.Errorf("check local output blob: %w", err))
				return
			}
			if !hasLocal {
				s.failTaskOutputIngest(resp, fmt.Errorf("local output blob %s is unavailable", outputHash))
				return
			}
		}
		s.Compute.RecordTaskResponse(*resp)
		return
	}

	downloadCtx, cancelLifetime := s.contextWithServerLifetime(ctx)
	defer cancelLifetime()
	dlCtx, dlCancel := context.WithTimeout(downloadCtx, PeerRPCBlobLong)
	err = s.fetchBlobFromPeer(dlCtx, producerNodeID, outputMeta)
	dlCancel()
	if err == nil {
		localPath := s.Storage.GetBlobPath(outputHash)
		resp.Outputs[protocol.ResultLocalPathKey] = localPath
		for k, v := range resp.Outputs {
			if pathStr, ok := v.(string); ok {
				if protocol.IsVFSURI(pathStr) || pathStr == outputHash {
					resp.Outputs[k] = localPath
				}
			}
		}
	} else {
		s.Config.Logger.Error("Failed to auto-download output blob", "hash", outputHash, "error", err)
		s.failTaskOutputIngest(resp, fmt.Errorf("output blob download failed: %w", err))
		return
	}
	s.Compute.RecordTaskResponse(*resp)
}

func (s *Server) LocalServiceRun(serviceName string, payloadStr string, sortStrategy ...string) (protocol.ServiceTaskResponse, error) {
	return s.LocalServiceRunContext(context.Background(), serviceName, payloadStr, sortStrategy...)
}

func (s *Server) LocalServiceRunContext(
	ctx context.Context,
	serviceName string,
	payloadStr string,
	sortStrategy ...string,
) (protocol.ServiceTaskResponse, error) {
	ctx, cancel := s.contextWithServerLifetime(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return protocol.ServiceTaskResponse{}, err
	}
	payload, err := parseServicePayload(payloadStr)
	if err != nil {
		return protocol.ServiceTaskResponse{}, err
	}
	strategy := ""
	if len(sortStrategy) > 0 {
		strategy = sortStrategy[0]
	}

	targetPeerID, err := s.resolveServiceBidTarget(serviceName, strategy)
	if err != nil {
		return protocol.ServiceTaskResponse{}, fmt.Errorf("failed to discover service: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return protocol.ServiceTaskResponse{}, err
	}

	taskID := fmt.Sprintf("task_kt_%d", time.Now().UnixNano())
	taskReq := protocol.TaskRequest{
		TaskID:                 taskID,
		Service:                serviceName,
		RequesterNodeID:        s.Config.ID,
		ReplyTo:                protocol.PeerHTTPSURL(s.Config.ID, protocol.PathServicesCallback),
		Payload:                payload,
		ExpectedProducerNodeID: targetPeerID,
	}
	if err := s.Compute.BindPipelineTask(&taskReq); err != nil {
		return protocol.ServiceTaskResponse{}, fmt.Errorf("bind pipeline task: %w", err)
	}

	if targetPeerID == s.Config.ID {
		err = s.submitTrackedTask(taskReq, func(prepared protocol.TaskRequest) error {
			return s.Compute.SubmitTask(prepared)
		})
		if err != nil {
			return protocol.ServiceTaskResponse{}, fmt.Errorf("failed to submit local task: %w", err)
		}
	} else {
		// DispatchTask owns RegisterOutgoingTask + MarkTaskAsFailed for remote submit.
		err = s.DispatchTask(targetPeerID, taskReq)
		if err != nil {
			return protocol.ServiceTaskResponse{}, err
		}
	}

	resp, err := s.waitTaskResponse(ctx, taskID, TaskWaitTimeout)
	if err != nil {
		return protocol.ServiceTaskResponse{}, err
	}
	s.ingestTaskOutputsContext(ctx, &resp, targetPeerID)
	if resp.Status == "failed" {
		errMsg := resp.Error
		if errMsg == "" && resp.Outputs != nil {
			if e, ok := resp.Outputs["error"].(string); ok {
				errMsg = e
			}
		}
		if errMsg == "" {
			errMsg = "task failed"
		}
		return resp, fmt.Errorf("%s", errMsg)
	}
	return resp, nil
}

func (s *Server) LocalServiceStreamRun(
	serviceName string,
	payloadStr string,
	chunkCallback func(chunk map[string]any),
) error {
	return s.LocalServiceStreamRunContext(
		context.Background(),
		serviceName,
		payloadStr,
		func(chunk map[string]any) error {
			if chunkCallback != nil {
				chunkCallback(chunk)
			}
			return nil
		},
	)
}

func (s *Server) LocalServiceStreamRunContext(
	ctx context.Context,
	serviceName string,
	payloadStr string,
	chunkCallback func(chunk map[string]any) error,
) error {
	ctx, cancel := s.contextWithServerLifetime(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := parseServicePayload(payloadStr)
	if err != nil {
		return err
	}
	return s.localServiceStreamRun(ctx, serviceName, payload, chunkCallback)
}

var errServiceStreamComplete = errors.New("service stream complete")

func (s *Server) localServiceStreamRun(
	ctx context.Context,
	serviceName string,
	payload map[string]any,
	chunkCallback func(chunk map[string]any) error,
) error {
	if schema, ok := s.Compute.GetService(serviceName); ok && !schema.IsStreaming() {
		return fmt.Errorf("service '%s' does not support streaming (type %q is unary)", serviceName, schema.Type)
	}

	handler, exists := s.Compute.GetHandler(serviceName)
	if !exists {
		targetPeerID, err := s.resolveServiceBidTarget(serviceName, "")
		if err != nil || targetPeerID == "" {
			return fmt.Errorf("streaming service '%s' is not registered on this node or cluster: %v", serviceName, err)
		}
		if targetPeerID == s.Config.ID {
			return fmt.Errorf("streaming service '%s' is not registered on this node", serviceName)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		streamCtx, cancel := context.WithTimeout(ctx, PeerRPCStream)
		defer cancel()
		var streamBody io.ReadCloser
		streamVersion := 0
		if negotiator, ok := s.peerClient.(p2p.ServiceStreamNegotiator); ok {
			negotiated, negotiateErr := negotiator.StreamServiceNegotiated(
				streamCtx,
				targetPeerID,
				serviceName,
				payload,
			)
			err = negotiateErr
			streamBody = negotiated.Body
			streamVersion = negotiated.Version
		} else {
			streamBody, err = s.peerClient.StreamService(streamCtx, targetPeerID, serviceName, payload)
		}
		if err != nil {
			if streamCtx.Err() != nil {
				return streamCtx.Err()
			}
			s.Config.Logger.Error("StreamService from remote peer failed", "peerID", targetPeerID, "service", serviceName, "error", err)
			return fmt.Errorf("failed to stream service from remote peer '%s': %w", targetPeerID, err)
		}
		if streamBody == nil {
			return fmt.Errorf("remote peer '%s' returned an empty stream body", targetPeerID)
		}
		defer func() { _ = streamBody.Close() }()
		stopClose := context.AfterFunc(streamCtx, func() { _ = streamBody.Close() })
		defer stopClose()

		if streamVersion != 0 && streamVersion != protocol.ServiceStreamVersion {
			return fmt.Errorf("unsupported negotiated remote service stream version %d", streamVersion)
		}
		err = utils.ForEachNDJSON(streamBody, func(chunk map[string]any) error {
			if streamVersion == protocol.ServiceStreamVersion {
				versionValue, framed := chunk["proxyma_stream_version"]
				if !framed {
					return fmt.Errorf("negotiated v1 remote service stream emitted a legacy frame")
				}
				version, ok := versionValue.(float64)
				if !ok || version != protocol.ServiceStreamVersion {
					return fmt.Errorf("unsupported remote service stream version %v", versionValue)
				}
				kindValue, ok := chunk["kind"].(string)
				if !ok {
					return fmt.Errorf("versioned remote stream frame has no valid kind")
				}
				switch protocol.ServiceStreamFrameKind(kindValue) {
				case protocol.ServiceStreamFrameComplete:
					return errServiceStreamComplete
				case protocol.ServiceStreamFrameError:
					message, _ := chunk["error"].(string)
					if message == "" {
						message = "remote stream failed"
					}
					return fmt.Errorf("%s", message)
				case protocol.ServiceStreamFrameChunk:
					data, ok := chunk["chunk"].(map[string]any)
					if !ok || data == nil {
						return fmt.Errorf("versioned remote stream chunk must contain an object")
					}
					chunk = data
				default:
					return fmt.Errorf("unknown remote stream frame kind %q", kindValue)
				}
			}
			if chunkCallback != nil {
				if err := chunkCallback(chunk); err != nil {
					return fmt.Errorf("stream chunk callback failed: %w", err)
				}
			}
			return nil
		})
		if streamCtx.Err() != nil {
			return streamCtx.Err()
		}
		if errors.Is(err, errServiceStreamComplete) {
			return nil
		}
		if err != nil {
			return err
		}
		if streamVersion == 0 {
			return nil
		}
		return fmt.Errorf("remote service stream ended without a terminal event: %w", io.ErrUnexpectedEOF)
	}

	streamCtx, cancel := context.WithTimeout(ctx, PeerRPCStream)
	defer cancel()
	in := make(chan map[string]any, 1)
	out := make(chan map[string]any, 10)
	errChan := make(chan error, 1)
	go func() {
		errChan <- handler.ExecuteStream(streamCtx, in, out)
	}()

	select {
	case <-streamCtx.Done():
		close(in)
		return streamCtx.Err()
	case in <- payload:
		close(in)
	}

	deliverChunk := func(chunk map[string]any) error {
		if streamCtx.Err() != nil {
			return streamCtx.Err()
		}
		if chunk == nil {
			return fmt.Errorf("stream chunk must be a JSON object")
		}
		if chunkCallback != nil {
			if err := chunkCallback(chunk); err != nil {
				return fmt.Errorf("stream chunk callback failed: %w", err)
			}
		}
		return nil
	}

	for {
		select {
		case <-streamCtx.Done():
			return streamCtx.Err()
		case handlerErr := <-errChan:
			for {
				select {
				case chunk, ok := <-out:
					if !ok {
						return handlerErr
					}
					if err := deliverChunk(chunk); err != nil {
						cancel()
						return err
					}
				default:
					if handlerErr != nil {
						return handlerErr
					}
					return fmt.Errorf("stream handler completed without closing output")
				}
			}
		case chunk, ok := <-out:
			if !ok {
				select {
				case err := <-errChan:
					return err
				case <-streamCtx.Done():
					return streamCtx.Err()
				}
			}
			if err := deliverChunk(chunk); err != nil {
				cancel()
				return err
			}
		}
	}
}
