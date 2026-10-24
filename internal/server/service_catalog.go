package server

import (
	"context"
	"errors"
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
	return s.LocalServiceDiscoverContext(s.lifetimeCtx)
}

func (s *Server) LocalServiceDiscoverContext(ctx context.Context) ([]string, error) {
	names := make(map[string]bool)
	protocol.PopulateMap(names, s.Compute.ListServices(), true)
	peerLists := mapEachPeer(s, forEachPeerOpts{Context: ctx, Timeout: PeerRPCDiscover, Parallel: true}, func(ctx context.Context, peerID string) ([]string, error) {
		peerSvc, err := s.DiscoverServices(ctx, peerID)
		if err != nil {
			s.Config.Logger.Warn("Service discovery from cluster peer failed", "peerID", peerID, "error", err)
			return nil, err
		}
		return peerSvc, nil
	})
	for _, peerSvc := range peerLists {
		protocol.PopulateMap(names, peerSvc, true)
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
		return schema, "", protocol.MissingParamError("name")
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

// commitCatalogMutation stages the gossip payload, applies the local mutation,
// and reconciles the staged outbox when the mutation fails (SSOT for Local*
// catalog mutations). Returns the joined mutation/finish error.
func (s *Server) commitCatalogMutation(kind gossipKind, entity string, payload any, apply func() error) error {
	staged, err := s.prepareOutboxMutation(kind, entity, payload)
	if err != nil {
		return err
	}
	mutationErr := apply()
	return errors.Join(mutationErr, staged.finish(mutationErr == nil))
}

func (s *Server) LocalServiceAdd(name, serviceType, exec, desc, param, noRequired, schemaFile string) (any, error) {
	serviceName, localService, err := compute.BuildLocalServiceFromArgs(name, serviceType, exec, desc, param, noRequired, schemaFile)
	if err != nil {
		return nil, err
	}
	payload := s.serviceNotification(
		protocol.ActionAdd,
		protocol.NormalizeServiceSchema(serviceName, localService.Schema, localService.Type),
	)
	if err := s.commitCatalogMutation(kindService, serviceName, payload, func() error {
		_, err := s.applyServiceAction(serviceName, &localService, protocol.ActionAdd)
		return err
	}); err != nil {
		return nil, err
	}
	s.goOwned(func() { s.Compute.WakeupEscrowTasks() })
	return protocol.APIMessage{Message: fmt.Sprintf("Service '%s' added successfully.", serviceName)}, nil
}

func (s *Server) LocalServiceRemove(name string) (any, error) {
	schema, _ := s.Compute.GetService(name)
	schema = protocol.NormalizeServiceSchema(name, schema, "")
	payload := s.serviceNotification(protocol.ActionRemove, schema)
	if err := s.commitCatalogMutation(kindService, name, payload, func() error {
		_, err := s.applyServiceAction(name, nil, protocol.ActionRemove)
		return err
	}); err != nil {
		return nil, err
	}
	return protocol.APIMessage{Message: fmt.Sprintf("Service '%s' removed successfully.", name)}, nil
}

func (s *Server) notifyService(ctx context.Context, peerID string, schema protocol.ServiceSchema, action string) error {
	payload := s.serviceNotification(action, schema)
	return s.notifyWithOutbox(ctx, peerID, kindService, schema.Name, payload)
}

func (s *Server) NotifyServiceToPeer(peerID string, schema protocol.ServiceSchema, action string) {
	s.gossipToPeer(peerID, func(ctx context.Context, peerID string) error {
		return s.notifyService(ctx, peerID, schema, action)
	})
}

// LocalServiceSubscribe records interest in a remote service name/pattern (L2).
// When any subscription exists, HandleServiceNotify only materializes matching schemas.
func (s *Server) LocalServiceSubscribe(pattern string, subscribe bool) error {
	if pattern == "" {
		return fmt.Errorf("missing service subscription pattern")
	}
	if err := s.Storage.SetServiceSubscription(pattern, subscribe); err != nil {
		return err
	}
	return nil
}
