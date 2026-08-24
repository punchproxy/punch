package tun

import (
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/punchproxy/punch/internal/eventbus"
	"github.com/punchproxy/punch/internal/session"
	singbuf "github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

type blockingHistoryStore struct {
	appendStarted chan struct{}
	releaseAppend chan struct{}
	startOnce     sync.Once
	releaseOnce   sync.Once
	mu            sync.Mutex
	records       []session.ClosedRecord
}

func newBlockingHistoryStore() *blockingHistoryStore {
	return &blockingHistoryStore{
		appendStarted: make(chan struct{}),
		releaseAppend: make(chan struct{}),
	}
}

func (s *blockingHistoryStore) AppendClosedSession(rec session.ClosedRecord, _ int) error {
	s.startOnce.Do(func() { close(s.appendStarted) })
	<-s.releaseAppend
	s.mu.Lock()
	s.records = append(s.records, rec)
	s.mu.Unlock()
	return nil
}

func (s *blockingHistoryStore) ListClosedSessions(_ int) ([]session.ClosedRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.ClosedRecord(nil), s.records...), nil
}

func (s *blockingHistoryStore) GetClosedSession(string) (session.ClosedRecord, bool, error) {
	return session.ClosedRecord{}, false, nil
}

func (s *blockingHistoryStore) ClearClosedSessions() (int, error) { return 0, nil }

func (s *blockingHistoryStore) release() {
	s.releaseOnce.Do(func() { close(s.releaseAppend) })
}

func newTestUDPPacket() *udpTunPacket {
	buffer := singbuf.NewPacket()
	buffer.Write([]byte("payload"))
	return &udpTunPacket{
		buffer:      buffer,
		destination: M.SocksaddrFrom(netip.MustParseAddr("127.0.0.1"), 53),
	}
}

func TestUDPSenderSendEnqueuesPacket(t *testing.T) {
	h := &handler{}
	sender := newUDPSender(h, "test", nil, M.Socksaddr{}, M.Socksaddr{})
	packet := newTestUDPPacket()

	sender.Send(packet)

	select {
	case got := <-sender.ch:
		if got != packet {
			t.Fatalf("queued packet = %p, want %p", got, packet)
		}
		got.buffer.Release()
	default:
		t.Fatal("packet was not queued")
	}
	stats := h.UDPStats()
	if stats.PacketsEnqueued != 1 || stats.PacketsDropped != 0 {
		t.Fatalf("UDPStats() = %+v, want 1 enqueued and 0 dropped", stats)
	}
}

func TestUDPSenderSendDropsClosedPacket(t *testing.T) {
	h := &handler{}
	sender := newUDPSender(h, "test", nil, M.Socksaddr{}, M.Socksaddr{})
	sender.Close()
	packet := newTestUDPPacket()

	sender.Send(packet)

	stats := h.UDPStats()
	if stats.PacketsDropped != 1 || stats.ClosedDrops != 1 || stats.QueueFullDrops != 0 {
		t.Fatalf("UDPStats() = %+v, want one closed drop", stats)
	}
}

func TestUDPSenderSendDropsAfterFullQueueTimeout(t *testing.T) {
	h := &handler{}
	sender := newUDPSender(h, "test", nil, M.Socksaddr{}, M.Socksaddr{})
	for i := 0; i < udpPacketQueueSize; i++ {
		sender.Send(newTestUDPPacket())
	}
	packet := newTestUDPPacket()

	start := time.Now()
	sender.Send(packet)
	elapsed := time.Since(start)

	if elapsed < udpPacketEnqueueTimeout/2 {
		t.Fatalf("Send() returned after %s, want bounded wait before overflow drop", elapsed)
	}
	stats := h.UDPStats()
	if stats.PacketsEnqueued != udpPacketQueueSize || stats.PacketsDropped != 1 || stats.QueueFullDrops != 1 {
		t.Fatalf("UDPStats() = %+v, want full queue drop after %d enqueues", stats, udpPacketQueueSize)
	}
	sender.dropPending()
}

func TestUDPSenderDropPendingCountsPendingDrops(t *testing.T) {
	h := &handler{}
	sender := newUDPSender(h, "test", nil, M.Socksaddr{}, M.Socksaddr{})
	sender.Send(newTestUDPPacket())
	sender.Send(newTestUDPPacket())

	sender.dropPending()

	stats := h.UDPStats()
	if stats.PendingDrops != 2 || stats.PacketsDropped != 2 {
		t.Fatalf("UDPStats() = %+v, want two pending drops", stats)
	}
}

func TestUDPSenderRegistryReplacesClosedSender(t *testing.T) {
	h := &handler{}
	registry := newUDPSenderRegistry()
	create := func() *udpSender {
		return newUDPSender(h, "test", nil, M.Socksaddr{}, M.Socksaddr{})
	}

	first, created := registry.getOrCreate("destination", create)
	if !created {
		t.Fatal("first sender was not created")
	}
	first.Close()

	second, created := registry.getOrCreate("destination", create)
	if !created {
		t.Fatal("closed sender was reused")
	}
	if second == first {
		t.Fatal("closed sender was not replaced")
	}

	// Cleanup from the old sender may arrive after its replacement was stored.
	// It must not remove the replacement.
	first.cleanup()
	got, created := registry.getOrCreate("destination", create)
	if created || got != second {
		t.Fatal("stale sender cleanup removed its replacement")
	}

	// Once the active sender finishes cleanup, the next packet gets a new one.
	second.cleanup()
	third, created := registry.getOrCreate("destination", create)
	if !created || third == second {
		t.Fatal("cleaned sender remained in the registry")
	}
	registry.close()
}

func TestUDPSenderRegistryCloseWaitsForSenderCleanup(t *testing.T) {
	h := &handler{}
	registry := newUDPSenderRegistry()
	sender, created := registry.getOrCreate("destination", func() *udpSender {
		return newUDPSender(h, "test", nil, M.Socksaddr{}, M.Socksaddr{})
	})
	if !created {
		t.Fatal("sender was not created")
	}
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	sender.onCleanup = func(*udpSender) {
		close(cleanupStarted)
		<-releaseCleanup
	}
	sender.Start()

	closeDone := make(chan struct{})
	go func() {
		registry.close()
		close(closeDone)
	}()

	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("sender cleanup did not start")
	}
	select {
	case <-closeDone:
		t.Fatal("registry close returned before sender cleanup finished")
	default:
	}
	close(releaseCleanup)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("registry close did not finish after sender cleanup")
	}
}

func TestHandlerCloseWaitsForSessionPersistence(t *testing.T) {
	store := newBlockingHistoryStore()
	defer store.release()
	sessions := session.NewManager(eventbus.New(), 10)
	sessions.SetHistoryStore(store)
	h := &handler{sessions: sessions}
	if !h.beginActivity() {
		t.Fatal("handler rejected activity before shutdown")
	}

	sess := sessions.NewSession("example.com", "127.0.0.1:1000", "198.18.0.1", 443, "TCP", "DIRECT", "", session.SessionOpts{})
	flowStop := make(chan struct{})
	sess.SetCloseFunc(func() { close(flowStop) })
	flowDone := make(chan struct{})
	go func() {
		defer close(flowDone)
		<-flowStop
		sessions.CloseSession(sess.ID, session.StatusClosed)
		h.endActivity()
	}()

	closeDone := make(chan struct{})
	go func() {
		_ = h.Close()
		close(closeDone)
	}()

	select {
	case <-store.appendStarted:
	case <-time.After(time.Second):
		t.Fatal("session persistence did not start during handler shutdown")
	}
	select {
	case <-closeDone:
		t.Fatal("handler close returned while session persistence was still running")
	default:
	}

	store.release()
	select {
	case <-flowDone:
	case <-time.After(time.Second):
		t.Fatal("session flow did not finish after persistence completed")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("handler close did not finish after session persistence completed")
	}
	if h.beginActivity() {
		h.endActivity()
		t.Fatal("handler accepted new activity after shutdown")
	}
	if records, _ := store.ListClosedSessions(10); len(records) != 1 || records[0].ID != sess.ID {
		t.Fatalf("persisted sessions = %+v, want session %s", records, sess.ID)
	}
}
