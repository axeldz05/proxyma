package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"proxyma/internal/protocol"
)

// ValidatePipelineSchema delegates to protocol.ValidatePipelineSchema (SSOT) with cached schema lookup.
func (s *Server) ValidatePipelineSchema(schema protocol.PipelineSchema) error {
	return protocol.ValidatePipelineSchema(schema, s.lookupCachedServiceSchema)
}

func (s *Server) parseAndValidatePipeline(schemaJSON string) (protocol.PipelineSchema, error) {
	var schema protocol.PipelineSchema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return schema, fmt.Errorf("invalid pipeline schema JSON: %w", err)
	}
	schema = protocol.NormalizePipelineSchemaVersion(schema)
	if err := s.ValidatePipelineSchema(schema); err != nil {
		return schema, fmt.Errorf("pipeline validation failed: %w", err)
	}
	return schema, nil
}

// applyPipelineAction persists and registers/unregisters a pipeline (L2 SSOT).
func (s *Server) applyPipelineAction(schema protocol.PipelineSchema, action string) error {
	schema = protocol.NormalizePipelineSchemaVersion(schema)
	switch action {
	case protocol.ActionAdd:
	case protocol.ActionRemove:
		schema.Deleted = true
	default:
		return fmt.Errorf("unknown pipeline action %q", action)
	}

	return s.Compute.ApplyPipelineRevision(schema, func(staged protocol.PipelineSchema) error {
		if s.Storage == nil {
			return nil
		}
		if err := s.Storage.SavePipelineSchema(staged); err != nil {
			return fmt.Errorf("failed to save pipeline schema revision to DB: %w", err)
		}
		return nil
	})
}

func (s *Server) LocalPipelineValidate(schemaJSON string) error {
	_, err := s.parseAndValidatePipeline(schemaJSON)
	return err
}

func (s *Server) LocalPipelineAdd(schemaJSON string) error {
	schema, err := s.parseAndValidatePipeline(schemaJSON)
	if err != nil {
		return err
	}
	schema.Deleted = false
	payload := protocol.PipelineNotification{NodeID: s.Config.ID, Schema: schema, Action: protocol.ActionAdd}
	staged, err := s.prepareOutboxMutation(kindPipeline, schema.ID, payload)
	if err != nil {
		return err
	}
	mutationErr := s.applyPipelineAction(schema, protocol.ActionAdd)
	return errors.Join(mutationErr, staged.finish(mutationErr == nil))
}

func (s *Server) LocalPipelineRemove(id string) error {
	if id == "" {
		return protocol.ErrEmptyPipelineID
	}
	schema, exists := s.Compute.GetPipeline(id)
	if !exists {
		schema, exists = s.Compute.GetPipelineRevision(id)
		if !exists {
			return fmt.Errorf("pipeline schema '%s' not found in cluster", id)
		}
	}
	schema.Deleted = true
	payload := protocol.PipelineNotification{NodeID: s.Config.ID, Schema: schema, Action: protocol.ActionRemove}
	staged, err := s.prepareOutboxMutation(kindPipeline, schema.ID, payload)
	if err != nil {
		return err
	}
	mutationErr := s.applyPipelineAction(schema, protocol.ActionRemove)
	return errors.Join(mutationErr, staged.finish(mutationErr == nil))
}

func (s *Server) LocalPipelineList() []protocol.PipelineSchema {
	return s.Compute.ListPipelines()
}

func (s *Server) LocalPipelineGet(id string) (protocol.PipelineSchema, error) {
	if id == "" {
		return protocol.PipelineSchema{}, protocol.ErrEmptyPipelineID
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

func (s *Server) notifyPipeline(ctx context.Context, peerID string, schema protocol.PipelineSchema, action string) error {
	schema = protocol.NormalizePipelineSchemaVersion(schema)
	payload := protocol.PipelineNotification{NodeID: s.Config.ID, Schema: schema, Action: action}
	return s.notifyWithOutbox(ctx, peerID, kindPipeline, schema.ID, payload, func(ctx context.Context) error {
		return s.peerClient.NotifyPipelineSchema(ctx, peerID, payload)
	})
}

func (s *Server) NotifySchemaToPeer(peerID string, schema protocol.PipelineSchema, action string) {
	s.gossipToPeer(peerID, func(ctx context.Context, peerID string) error {
		return s.notifyPipeline(ctx, peerID, schema, action)
	})
}

func (s *Server) NotifySchema(schema protocol.PipelineSchema, action string) {
	s.gossipAll(func(ctx context.Context, peerID string) error {
		return s.notifyPipeline(ctx, peerID, schema, action)
	})
}
