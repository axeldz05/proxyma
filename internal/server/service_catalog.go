package server

import (
	"context"
	"fmt"
	"os"
	"proxyma/internal/compute"
	"proxyma/internal/protocol"
	"sort"
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

		svc.Schema = protocol.NormalizeServiceSchema(name, svc.Schema, svc.Type)

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
	peerLists := mapEachPeer(s, forEachPeerOpts{Timeout: PeerRPCDiscover, Parallel: true}, func(ctx context.Context, peerID string) ([]string, error) {
		peerSvc, err := s.DiscoverServices(ctx, peerID)
		if err != nil {
			s.Config.Logger.Warn("Service discovery from cluster peer failed", "peerID", peerID, "error", err)
			return nil, err
		}
		return peerSvc, nil
	})
	for _, peerSvc := range peerLists {
		for _, name := range peerSvc {
			names[name] = true
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	s.Config.Logger.Info("Service discovery scan completed", "peers_scanned", len(s.GetPeersCopy()), "services_found", len(result))
	return result, nil
}

// LocalServiceDetail resolves a service schema locally or via cluster bidding (SSOT).
func (s *Server) LocalServiceDetail(name string) (schema protocol.ServiceSchema, addr string, err error) {
	if name == "" {
		return schema, "", fmt.Errorf("missing name parameter")
	}
	var exists bool
	schema, exists = s.Compute.GetService(name)
	if exists {
		return schema, "", nil
	}
	_, addr, schema, err = s.RequestServiceToCluster(protocol.DiscoveryQuery{Service: name})
	return schema, addr, err
}

// applyServiceAction persists and reloads local services (L2 SSOT, mirrors applyPipelineAction).
func (s *Server) applyServiceAction(name string, localService *protocol.LocalService, action string) (protocol.ServiceSchema, error) {
	switch action {
	case protocol.ActionAdd:
		if localService == nil {
			return protocol.ServiceSchema{}, fmt.Errorf("local service required for add")
		}
		if err := compute.UpsertLocalService(s.Config.StoragePath, name, *localService); err != nil {
			return protocol.ServiceSchema{}, fmt.Errorf("error saving services file: %w", err)
		}
		s.LoadLocalServices()
		return protocol.NormalizeServiceSchema(name, localService.Schema, localService.Type), nil
	case protocol.ActionRemove:
		schema, _ := s.Compute.GetService(name)
		schema = protocol.NormalizeServiceSchema(name, schema, "")
		if err := compute.DeleteLocalService(s.Config.StoragePath, name); err != nil {
			return protocol.ServiceSchema{}, err
		}
		s.LoadLocalServices()
		return schema, nil
	default:
		return protocol.ServiceSchema{}, fmt.Errorf("unknown service action %q", action)
	}
}

func (s *Server) LocalServiceAdd(name, serviceType, exec, desc, param, noRequired, schemaFile string) (string, error) {
	serviceName, localService, err := compute.BuildLocalServiceFromArgs(name, serviceType, exec, desc, param, noRequired, schemaFile)
	if err != nil {
		return "", err
	}
	schema, err := s.applyServiceAction(serviceName, &localService, protocol.ActionAdd)
	if err != nil {
		return "", err
	}
	go s.NotifyService(schema, protocol.ActionAdd)
	return fmt.Sprintf("Service '%s' added successfully.", serviceName), nil
}

func (s *Server) LocalServiceRemove(name string) (string, error) {
	schema, err := s.applyServiceAction(name, nil, protocol.ActionRemove)
	if err != nil {
		return "", err
	}
	go s.NotifyService(schema, protocol.ActionRemove)
	return fmt.Sprintf("Service '%s' removed successfully.", name), nil
}

func (s *Server) notifyService(ctx context.Context, peerID string, schema protocol.ServiceSchema, action string) error {
	payload := protocol.ServiceNotification{
		Action: action,
		NodeID: s.Config.ID,
		Schema: schema,
	}
	err := s.peerClient.NotifyServiceUpdate(ctx, peerID, payload)
	if err != nil {
		s.Config.Logger.Debug("Failed to notify peer about service update", "peerID", peerID, "service", schema.Name, "error", err)
	}
	return err
}

func (s *Server) NotifyServiceToPeer(peerID string, schema protocol.ServiceSchema, action string) {
	ctx, cancel := context.WithTimeout(context.Background(), PeerRPCDefault)
	defer cancel()
	_ = s.callPeer(ctx, peerID, func(ctx context.Context, peerID string) error {
		return s.notifyService(ctx, peerID, schema, action)
	})
}

func (s *Server) NotifyService(schema protocol.ServiceSchema, action string) {
	s.forEachPeer(forEachPeerOpts{Timeout: PeerRPCDefault, Parallel: true}, func(ctx context.Context, peerID string) error {
		return s.notifyService(ctx, peerID, schema, action)
	})
}
