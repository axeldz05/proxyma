package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"proxyma/internal/protocol"
	"proxyma/internal/utils"
	"time"
)

func parseServicePayload(payloadStr string) map[string]any {
	var payload map[string]any
	if payloadStr != "" {
		_ = json.Unmarshal([]byte(payloadStr), &payload)
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	return payload
}

func (s *Server) waitTaskResponse(taskID string, timeout time.Duration) (protocol.ServiceTaskResponse, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		r, ok := s.Compute.GetTaskResponse(taskID)
		if ok && (r.Status == "completed" || r.Status == "failed") {
			return r, nil
		}
	}
	return protocol.ServiceTaskResponse{}, fmt.Errorf("task timed out on execution")
}

func (s *Server) ingestTaskOutputs(resp *protocol.ServiceTaskResponse, targetPeerID string) {
	if resp.Status != "completed" || resp.Outputs == nil {
		return
	}
	outputHash, outputName, outputSize := protocol.OutputHashFromOutputs(resp.Outputs)
	if outputHash == "" {
		return
	}
	if outputName == "" {
		outputName = outputHash + ".pdf"
	}
	outputMeta := s.Storage.UpsertAndSubscribe(protocol.IndexEntry{
		Name: outputName,
		Hash: outputHash,
		Size: outputSize,
	}, false)

	if targetPeerID == "" || targetPeerID == s.Config.ID {
		rawLocalPath := protocol.ResultLocalPath(resp.Outputs)
		if rawLocalPath != "" {
			if _, err := os.Stat(rawLocalPath); err == nil {
				if f, err := os.Open(rawLocalPath); err == nil {
					_ = s.Storage.SaveLocalFile(outputName, f)
					_ = f.Close()
				}
			}
		}
		return
	}

	dlCtx, dlCancel := context.WithTimeout(context.Background(), PeerRPCBlobLong)
	err := s.fetchBlobFromPeer(dlCtx, targetPeerID, outputMeta)
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
	}
}

func (s *Server) LocalServiceRun(serviceName string, payloadStr string, sortStrategy ...string) (protocol.ServiceTaskResponse, error) {
	payload := parseServicePayload(payloadStr)
	strategy := ""
	if len(sortStrategy) > 0 {
		strategy = sortStrategy[0]
	}

	targetPeerID, err := s.resolveServiceBidTarget(serviceName, strategy)
	if err != nil {
		return protocol.ServiceTaskResponse{}, fmt.Errorf("failed to discover service: %w", err)
	}

	taskID := fmt.Sprintf("task_kt_%d", time.Now().UnixNano())
	taskReq := protocol.TaskRequest{
		TaskID:          taskID,
		Service:         serviceName,
		RequesterNodeID: s.Config.ID,
		ReplyTo:         fmt.Sprintf("https://%s.proxyma.local%s", s.Config.ID, protocol.PathServicesCallback),
		Payload:         payload,
	}

	if targetPeerID == s.Config.ID {
		err = s.submitTrackedTask(taskReq, func() error {
			return s.Compute.SubmitTask(taskReq)
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

	resp, err := s.waitTaskResponse(taskID, TaskWaitTimeout)
	if err != nil {
		return protocol.ServiceTaskResponse{}, err
	}
	s.ingestTaskOutputs(&resp, targetPeerID)
	return resp, nil
}

func (s *Server) LocalServiceStreamRun(serviceName string, payloadStr string, chunkCallback func(chunk map[string]any)) error {
	payload := parseServicePayload(payloadStr)

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

		ctx, cancel := context.WithTimeout(context.Background(), PeerRPCStream)
		defer cancel()

		streamBody, err := s.peerClient.StreamService(ctx, targetPeerID, serviceName, payload)
		if err != nil {
			s.Config.Logger.Error("StreamService from remote peer failed", "peerID", targetPeerID, "service", serviceName, "error", err)
			return fmt.Errorf("failed to stream service from remote peer '%s': %w", targetPeerID, err)
		}
		defer func() { _ = streamBody.Close() }()

		return utils.ForEachNDJSON(streamBody, func(chunk map[string]any) error {
			if chunkCallback != nil {
				chunkCallback(chunk)
			}
			return nil
		})
	}

	in := make(chan map[string]any, 1)
	out := make(chan map[string]any, 10)
	errChan := make(chan error, 1)

	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCStream)
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
