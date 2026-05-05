package tun

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
)

type fakePacketAdapter struct {
	dropped atomic.Int64
}

func (p *fakePacketAdapter) Data() []byte { return []byte("payload") }

func (p *fakePacketAdapter) WriteBack(b []byte, _ net.Addr) (int, error) { return len(b), nil }

func (p *fakePacketAdapter) Drop() { p.dropped.Add(1) }

func (p *fakePacketAdapter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}

func (p *fakePacketAdapter) Metadata() *C.Metadata { return &C.Metadata{} }

func (p *fakePacketAdapter) Key() string { return "127.0.0.1:12345" }

func TestUDPSenderSendEnqueuesPacket(t *testing.T) {
	h := &handler{}
	sender := newUDPSender(h, "test")
	packet := &fakePacketAdapter{}

	sender.Send(packet)

	if got := packet.dropped.Load(); got != 0 {
		t.Fatalf("packet dropped = %d, want 0", got)
	}
	select {
	case got := <-sender.ch:
		if got != packet {
			t.Fatalf("queued packet = %p, want %p", got, packet)
		}
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
	sender := newUDPSender(h, "test")
	sender.Close()
	packet := &fakePacketAdapter{}

	sender.Send(packet)

	if got := packet.dropped.Load(); got != 1 {
		t.Fatalf("packet dropped = %d, want 1", got)
	}
	stats := h.UDPStats()
	if stats.PacketsDropped != 1 || stats.ClosedDrops != 1 || stats.QueueFullDrops != 0 {
		t.Fatalf("UDPStats() = %+v, want one closed drop", stats)
	}
}

func TestUDPSenderSendDropsAfterFullQueueTimeout(t *testing.T) {
	h := &handler{}
	sender := newUDPSender(h, "test")
	for i := 0; i < udpPacketQueueSize; i++ {
		sender.Send(&fakePacketAdapter{})
	}
	packet := &fakePacketAdapter{}

	start := time.Now()
	sender.Send(packet)
	elapsed := time.Since(start)

	if elapsed < udpPacketEnqueueTimeout/2 {
		t.Fatalf("Send() returned after %s, want bounded wait before overflow drop", elapsed)
	}
	if got := packet.dropped.Load(); got != 1 {
		t.Fatalf("packet dropped = %d, want 1", got)
	}
	stats := h.UDPStats()
	if stats.PacketsEnqueued != udpPacketQueueSize || stats.PacketsDropped != 1 || stats.QueueFullDrops != 1 {
		t.Fatalf("UDPStats() = %+v, want full queue drop after %d enqueues", stats, udpPacketQueueSize)
	}
}

func TestUDPSenderDropPendingCountsPendingDrops(t *testing.T) {
	h := &handler{}
	sender := newUDPSender(h, "test")
	first := &fakePacketAdapter{}
	second := &fakePacketAdapter{}
	sender.Send(first)
	sender.Send(second)

	sender.dropPending()

	if got := first.dropped.Load(); got != 1 {
		t.Fatalf("first packet dropped = %d, want 1", got)
	}
	if got := second.dropped.Load(); got != 1 {
		t.Fatalf("second packet dropped = %d, want 1", got)
	}
	stats := h.UDPStats()
	if stats.PendingDrops != 2 || stats.PacketsDropped != 2 {
		t.Fatalf("UDPStats() = %+v, want two pending drops", stats)
	}
}
