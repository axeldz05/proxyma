package server

import (
	"crypto/x509"
	"errors"
	"sort"
	"sync"
	"testing"

	"proxyma/internal/protocol"
	"proxyma/internal/testutil"
)

func newTestRegistry(t *testing.T) *PeerRegistry {
	t.Helper()
	return NewPeerRegistry(protocol.NewLogger(testutil.TestLogWriter{T: t}, true), "self")
}

func record(seq int64, addrs ...string) protocol.AddressRecord {
	return protocol.AddressRecord{Addresses: addrs, Sequence: seq}
}

// Certificates and service pushes arrive for peers that never announced an address
// (including the "sponsor" and "bootstrap" CNs). Holding state for such a peer must
// not make it look registered: mTLSGuard and the relay gate on GetPeerRecord.
func TestPeerStateWithoutRecordIsNotRegistered(t *testing.T) {
	pr := newTestRegistry(t)

	pr.SetPeerCertificate("stranger", &x509.Certificate{})
	pr.UpdatePeerService("stranger", protocol.ActionAdd, protocol.ServiceSchema{Name: "svc"})
	pr.SetPeerOffline("stranger", errors.New("never contacted"))

	if _, ok := pr.GetPeerRecord("stranger"); ok {
		t.Error("a peer that never announced an address must not be registered")
	}
	if _, ok := pr.GetPeersCopy()["stranger"]; ok {
		t.Error("GetPeersCopy must only list peers with an address record")
	}
	if _, ok := pr.GetPeersRecordCopy()["stranger"]; ok {
		t.Error("GetPeersRecordCopy must only list peers with an address record")
	}
	if _, ok := pr.GetSponsorPeers()["stranger"]; ok {
		t.Error("GetSponsorPeers must only list peers with an address record")
	}

	// The side state itself is still readable.
	if _, ok := pr.GetPeerCertificate("stranger"); !ok {
		t.Error("certificate stored for an unregistered peer must remain readable")
	}
	if _, ok := pr.GetClusterServices("stranger")["svc"]; !ok {
		t.Error("services stored for an unregistered peer must remain readable")
	}
	if got := pr.GetPeerError("stranger"); got == "" {
		t.Error("offline error must be stored for an unregistered peer")
	}
}

func TestGetPeerCertificateAbsent(t *testing.T) {
	pr := newTestRegistry(t)
	pr.SetPeerOffline("known", nil)

	if _, ok := pr.GetPeerCertificate("known"); ok {
		t.Error("a peer with state but no certificate must report none")
	}
	if _, ok := pr.GetPeerCertificate("unknown"); ok {
		t.Error("an unknown peer must report no certificate")
	}
}

func TestRemovePeerClearsAllState(t *testing.T) {
	pr := newTestRegistry(t)

	pr.AddPeer("peer-a", record(1, "https://a:8443"))
	pr.SetPeerCertificate("peer-a", &x509.Certificate{})
	pr.UpdatePeerService("peer-a", protocol.ActionAdd, protocol.ServiceSchema{Name: "svc"})
	pr.SetPeerOffline("peer-a", errors.New("boom"))

	pr.RemovePeer("peer-a")

	if _, ok := pr.GetPeerRecord("peer-a"); ok {
		t.Error("record survived RemovePeer")
	}
	if _, ok := pr.GetPeerCertificate("peer-a"); ok {
		t.Error("certificate survived RemovePeer")
	}
	if len(pr.GetClusterServices("peer-a")) != 0 {
		t.Error("services survived RemovePeer")
	}
	if got := pr.GetPeerError("peer-a"); got != "" {
		t.Errorf("error survived RemovePeer: %q", got)
	}
	if pr.IsPeerOnline("peer-a") {
		t.Error("online flag survived RemovePeer")
	}
	if _, ok := pr.GetServiceSchema("svc"); ok {
		t.Error("cluster service lookup still resolves a removed peer")
	}
}

// A peer reconnecting under a new ID from the same address evicts the old entry
// entirely, not just its address record.
func TestAddPeerEvictsStaleSameAddress(t *testing.T) {
	pr := newTestRegistry(t)

	pr.AddPeer("old-id", record(1, "https://node:8443"))
	pr.SetPeerCertificate("old-id", &x509.Certificate{})
	pr.UpdatePeerService("old-id", protocol.ActionAdd, protocol.ServiceSchema{Name: "svc"})

	pr.AddPeer("new-id", record(1, "https://node:8443"))

	if _, ok := pr.GetPeerRecord("old-id"); ok {
		t.Error("stale peer kept its record")
	}
	if _, ok := pr.GetPeerCertificate("old-id"); ok {
		t.Error("stale peer kept its certificate")
	}
	if len(pr.GetClusterServices("old-id")) != 0 {
		t.Error("stale peer kept its services")
	}
	if _, ok := pr.GetPeerRecord("new-id"); !ok {
		t.Error("new peer was not registered")
	}
}

func TestAddPeerSequenceRules(t *testing.T) {
	pr := newTestRegistry(t)

	if !pr.AddPeer("peer-a", record(5, "https://a:8443")) {
		t.Fatal("first AddPeer must report an update")
	}
	if pr.AddPeer("peer-a", record(4, "https://stale:8443")) {
		t.Error("an older sequence must be ignored")
	}
	if got, _ := pr.GetPeerRecord("peer-a"); got.Addresses[0] != "https://a:8443" {
		t.Errorf("older record overwrote the current one: %v", got.Addresses)
	}

	// Same sequence unions the address sets.
	pr.AddPeer("peer-a", record(5, "https://a-alt:8443"))
	got, _ := pr.GetPeerRecord("peer-a")
	addrs := append([]string(nil), got.Addresses...)
	sort.Strings(addrs)
	if len(addrs) != 2 || addrs[0] != "https://a-alt:8443" || addrs[1] != "https://a:8443" {
		t.Errorf("equal sequence must union addresses, got %v", addrs)
	}

	// A newer sequence replaces the set outright.
	pr.AddPeer("peer-a", record(6, "https://a-new:8443"))
	got, _ = pr.GetPeerRecord("peer-a")
	if len(got.Addresses) != 1 || got.Addresses[0] != "https://a-new:8443" {
		t.Errorf("newer sequence must replace addresses, got %v", got.Addresses)
	}
}

func TestAddPeerIgnoresSelf(t *testing.T) {
	pr := newTestRegistry(t)
	if pr.AddPeer("self", record(1, "https://self:8443")) {
		t.Error("the node must not register itself as a peer")
	}
	if _, ok := pr.GetPeerRecord("self"); ok {
		t.Error("self must not appear in the registry")
	}
}

func TestSetPeerOnlineClearsError(t *testing.T) {
	pr := newTestRegistry(t)

	pr.SetPeerOffline("peer-a", errors.New("connection refused"))
	if got := pr.GetPeerError("peer-a"); got != "offline or could not reach: connection refused" {
		t.Errorf("unexpected error text %q", got)
	}

	pr.SetPeerOnline("peer-a", true)
	if !pr.IsPeerOnline("peer-a") || pr.GetPeerError("peer-a") != "" {
		t.Error("marking a peer online must clear its stored error")
	}

	pr.SetPeerOnline("peer-a", false)
	if pr.IsPeerOnline("peer-a") || pr.GetPeerError("peer-a") != "offline" {
		t.Error("SetPeerOnline(false) must delegate to SetPeerOffline")
	}
}

// One lock now covers axes that used to have their own mutex; hammer them together
// so `go test -race -run TestPeerRegistryConcurrent` can prove the merge is sound.
// The wider suite cannot run under -race because boltdb v1.3.1 trips checkptr.
func TestPeerRegistryConcurrentAccess(t *testing.T) {
	pr := newTestRegistry(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := "peer-a"
			for j := 0; j < 200; j++ {
				pr.AddPeer(id, record(int64(j), "https://a:8443"))
				pr.SetPeerCertificate(id, &x509.Certificate{})
				pr.UpdatePeerService(id, protocol.ActionAdd, protocol.ServiceSchema{Name: "svc"})
				pr.SetPeerOffline(id, errors.New("flap"))
				pr.GetPeersCopy()
				pr.GetPeersRecordCopy()
				pr.GetSponsorPeers()
				pr.GetClusterServices(id)
				pr.GetServiceSchema("svc")
				pr.IsPeerOnline(id)
				pr.GetPeerError(id)
				pr.GetPeerCertificate(id)
				if n%4 == 3 {
					pr.RemovePeer(id)
				}
			}
		}(i)
	}
	wg.Wait()
}
