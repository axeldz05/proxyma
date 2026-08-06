package server

import (
	"net/http"
	"proxyma/internal/protocol"
)

func (s *Server) MountHandlers() http.Handler {
	mux := http.NewServeMux()
	// --- DOMINIO DE ALMACENAMIENTO (StorageEngine) ---
	mux.HandleFunc("POST "+protocol.PathUpload, s.Storage.HandleUpload)
	mux.HandleFunc("GET "+protocol.PathDownloadPrefix, s.Storage.HandleDownload)
	mux.HandleFunc("DELETE "+protocol.PathFile, s.Storage.HandleDelete)
	mux.HandleFunc("GET "+protocol.PathManifest, s.Storage.HandleManifest)
	mux.HandleFunc("POST "+protocol.PathSubscribe, s.Storage.HandleSubscribe)
	mux.HandleFunc("POST "+protocol.PathNotify, s.Storage.HandleNotification)

	mux.HandleFunc("POST "+protocol.PathServicesBid, s.Compute.HandleServiceBid)
	mux.HandleFunc("POST "+protocol.PathServicesSubmit, s.Compute.HandleServiceSubmit)
	mux.HandleFunc("POST "+protocol.PathServicesStream, s.HandleServicesStream)
	mux.HandleFunc("POST "+protocol.PathServicesCallback, s.Compute.HandleServiceCallback)
	mux.HandleFunc("POST "+protocol.PathServicesNotify, s.HandleServiceNotify)
	mux.HandleFunc("GET "+protocol.PathServices, s.HandleGetServices)
	mux.HandleFunc("POST "+protocol.PathSchemasNotify, s.HandleSchemaNotify)

	mux.HandleFunc("GET "+protocol.PathPeers, s.GetPeers)
	mux.HandleFunc("POST "+protocol.PathPeersAnnounce, s.HandleAnnounce)
	mux.HandleFunc("POST "+protocol.PathPeersAdd, s.HandleAddPeer)
	mux.HandleFunc("POST "+protocol.PathPeersLeave, s.HandleLeavePeer)
	mux.HandleFunc("POST "+protocol.PathPeersOffline, s.HandleOfflinePeer)
	mux.HandleFunc("POST "+protocol.PathPeersInvite, s.HandleGenerateInvite)
	mux.HandleFunc("POST "+protocol.PathPeersProbe, s.HandleProbe)
	mux.HandleFunc("POST "+protocol.PathClusterJoin, s.HandleClusterJoin)
	mux.HandleFunc("POST "+protocol.PathClusterRotate, s.HandleClusterRotate)
	mux.HandleFunc("GET "+protocol.PathRelayPoll, s.HandleRelayPoll)
	mux.HandleFunc("POST "+protocol.PathRelayForward, s.HandleRelayForward)
	mux.HandleFunc("POST "+protocol.PathRelayReply, s.HandleRelayReply)
	mux.HandleFunc("GET "+protocol.PathTelemetry, s.HandleTelemetry)
	mux.HandleFunc("POST "+protocol.PathHolePunchInit, s.HandleHolePunchInit)
	mux.HandleFunc("POST "+protocol.PathWebRTCSignal, s.HandleWebRTCSignal)
	// Bandwidth wrap once here so httptest fixtures and ListenAndServe share the same stack.
	return s.wrapWithBandwidthCounting(s.mTLSGuard(mux))
}
