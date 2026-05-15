package tun

import (
	"net/netip"
	"testing"
	"time"

	singbuf "github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

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
