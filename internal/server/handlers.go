package server

import (
	"net/http"
)

func (s *Server) MountHandlers() http.Handler {
	mux := http.NewServeMux()
	// --- DOMINIO DE ALMACENAMIENTO (StorageEngine) ---
	mux.HandleFunc("POST /upload", s.Storage.HandleUpload)
	mux.HandleFunc("GET /download/", s.Storage.HandleDownload)
	mux.HandleFunc("DELETE /file", s.Storage.HandleDelete)
	mux.HandleFunc("GET /manifest", s.Storage.HandleManifest)
	mux.HandleFunc("POST /subscribe", s.Storage.HandleSubscribe)
	mux.HandleFunc("POST /notify", s.Storage.HandleNotification)

	mux.HandleFunc("POST /services/bid", s.Compute.HandleServiceBid)
	mux.HandleFunc("POST /services/submit", s.Compute.HandleServiceSubmit)
	mux.HandleFunc("POST /services/stream", s.HandleServicesStream)
	mux.HandleFunc("POST /services/callback", s.Compute.HandleServiceCallback)
	mux.HandleFunc("POST /services/notify", s.HandleServiceNotify)
	mux.HandleFunc("GET /services", s.HandleGetServices)
	mux.HandleFunc("POST /schemas/notify", s.HandleSchemaNotify)

	mux.HandleFunc("GET /peers", s.GetPeers)
	mux.HandleFunc("POST /peers/announce", s.HandleAnnounce)
	mux.HandleFunc("POST /peers/add", s.HandleAddPeer)
	mux.HandleFunc("POST /peers/leave", s.HandleLeavePeer)
	mux.HandleFunc("POST /peers/offline", s.HandleOfflinePeer)
	mux.HandleFunc("POST /peers/invite", s.HandleGenerateInvite)
	mux.HandleFunc("POST /peers/probe", s.HandleProbe)
	mux.HandleFunc("POST /cluster/join", s.HandleClusterJoin)
	mux.HandleFunc("POST /cluster/rotate", s.HandleClusterRotate)
	mux.HandleFunc("GET /relay/poll", s.HandleRelayPoll)
	mux.HandleFunc("POST /relay/forward", s.HandleRelayForward)
	mux.HandleFunc("POST /relay/reply", s.HandleRelayReply)
	mux.HandleFunc("GET /telemetry", s.HandleTelemetry)
	mux.HandleFunc("POST /holepunch/init", s.HandleHolePunchInit)
	return s.mTLSGuard(mux)
}
