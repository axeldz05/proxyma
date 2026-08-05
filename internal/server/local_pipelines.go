package server

import (
	"context"
	"encoding/json"
	"fmt"
	"proxyma/internal/protocol"
	"sort"
	"strings"
)

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
	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
	defer cancel()
	err := s.callPeer(ctx, peerID, func(ctx context.Context, peerID string) error {
		return s.peerClient.NotifyPipelineSchema(ctx, peerID, payload)
	})
	if err != nil {
		s.Config.Logger.Debug("Failed to notify peer about schema update", "peerID", peerID, "pipelineID", schema.ID, "error", err)
	}
}

func (s *Server) NotifySchema(schema protocol.PipelineSchema, action string) {
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCDefault, Parallel: true}, func(ctx context.Context, peerID string) error {
		payload := protocol.PipelineNotification{
			Schema: schema,
			Action: action,
		}
		err := s.peerClient.NotifyPipelineSchema(ctx, peerID, payload)
		if err != nil {
			s.Config.Logger.Debug("Failed to notify peer about schema update", "peerID", peerID, "pipelineID", schema.ID, "error", err)
		}
		return err
	})
}
