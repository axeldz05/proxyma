package server

import (
	"sort"
)

// SponsorCandidate is one sponsor-capable peer as seen by the registry.
type SponsorCandidate struct {
	ID     string
	Addr   string
	Online bool
}

// SponsorSelector picks the next sponsor from the registry's candidate list.
// It never returns currentAddr and reports ok=false when no alternative
// exists. Implementations must be deterministic for a given input.
type SponsorSelector interface {
	Select(candidates []SponsorCandidate, currentAddr string) (SponsorCandidate, bool)
}

// FirstOnlineSponsorSelector is the v1 strategy: a stable order (sorted by
// peer ID) preferring online candidates, falling back to offline ones only
// when nothing is online. Deterministic, zero-cost, and the hook future
// strategies (e.g. latency-measured "fastest") slot behind the interface.
type FirstOnlineSponsorSelector struct{}

func (FirstOnlineSponsorSelector) Select(candidates []SponsorCandidate, currentAddr string) (SponsorCandidate, bool) {
	ordered := make([]SponsorCandidate, len(candidates))
	copy(ordered, candidates)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	var fallback SponsorCandidate
	hasFallback := false
	for _, cand := range ordered {
		if cand.Addr == currentAddr || cand.Addr == "" {
			continue
		}
		if cand.Online {
			return cand, true
		}
		if !hasFallback {
			fallback = cand
			hasFallback = true
		}
	}
	return fallback, hasFallback
}

// sponsorCandidates snapshots the registry's sponsor-capable peers.
func (s *Server) sponsorCandidates() []SponsorCandidate {
	sponsors := s.GetSponsorPeers()
	candidates := make([]SponsorCandidate, 0, len(sponsors))
	for id, addr := range sponsors {
		candidates = append(candidates, SponsorCandidate{ID: id, Addr: addr, Online: s.IsPeerOnline(id)})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	return candidates
}

// selectSponsor applies the configured strategy to the current registry
// snapshot. currentAddr may be empty on first adoption.
func (s *Server) selectSponsor(currentAddr string) (string, bool) {
	selector := s.sponsorSelector
	if selector == nil {
		selector = FirstOnlineSponsorSelector{}
	}
	cand, ok := selector.Select(s.sponsorCandidates(), currentAddr)
	if !ok {
		return "", false
	}
	return cand.Addr, true
}

// sponsorStillCapable reports whether addr still belongs to a registry peer
// that announces itself as a sponsor.
func (s *Server) sponsorStillCapable(addr string) bool {
	if addr == "" {
		return false
	}
	for _, cand := range s.sponsorCandidates() {
		if cand.Addr == addr {
			return true
		}
	}
	return false
}

// reconcileSponsor keeps the adopted sponsor aligned with the registry after
// topology updates: adopt when none is set, reselect when the current sponsor
// is no longer sponsor-capable. A healthy capable sponsor is never displaced
// proactively (anti-flapping); only absence, failure or capability loss moves it.
func (s *Server) reconcileSponsor() {
	if s.peerClient == nil {
		return
	}
	current := s.peerClient.CurrentSponsorAddress()
	if current == "" {
		if addr, ok := s.selectSponsor(""); ok {
			s.peerClient.UpdateSponsorAddress(addr)
			s.Config.Logger.Info("Sponsor adopted from registry", "sponsor", addr)
		}
		return
	}
	if s.sponsorStillCapable(current) {
		return
	}
	if addr, ok := s.selectSponsor(current); ok {
		s.Config.Logger.Info("Current sponsor is no longer sponsor-capable; switching", "old", current, "new", addr)
		s.peerClient.UpdateSponsorAddress(addr)
	}
}
