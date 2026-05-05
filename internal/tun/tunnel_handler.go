package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/nat"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/constant/provider"
	pdns "github.com/punchproxy/punch/internal/dns"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
)

const udpAssociationTimeout = 2 * time.Minute

const (
	udpPacketQueueSize      = 128
	udpPacketEnqueueTimeout = 5 * time.Millisecond
)

// UDPStats reports TUN UDP queue behavior.
type UDPStats struct {
	PacketsEnqueued int64 `json:"packets_enqueued"`
	PacketsDropped  int64 `json:"packets_dropped"`
	QueueFullDrops  int64 `json:"queue_full_drops"`
	ClosedDrops     int64 `json:"closed_drops"`
	PendingDrops    int64 `json:"pending_drops"`
}

type udpCounters struct {
	packetsEnqueued atomic.Int64
	queueFullDrops  atomic.Int64
	closedDrops     atomic.Int64
	pendingDrops    atomic.Int64
}

type handler struct {
	dnsServer *pdns.Server
	selector  *relay.Selector
	sessions  *session.Manager
	natTable  C.NatTable
	callbacks *utils.Callback[provider.RuleProvider]
	udp       udpCounters

	closeOnce sync.Once
}

func newHandler(dnsServer *pdns.Server, selector *relay.Selector, sessions *session.Manager) *handler {
	return &handler{
		dnsServer: dnsServer,
		selector:  selector,
		sessions:  sessions,
		natTable:  nat.New(),
		callbacks: utils.NewCallback[provider.RuleProvider](),
	}
}

func (h *handler) HandleTCPConn(conn net.Conn, metadata *C.Metadata) {
	defer conn.Close()

	target, domain, dstIP, rule, err := h.targetForMetadata(metadata)
	if err != nil {
		slog.Debug("drop invalid TCP TUN connection", "error", err)
		return
	}

	relayName := h.selector.ActiveName()
	opts := h.sessionOpts(rule, domain, dstIP)
	sess := h.sessions.NewSession(domain, sourceAddress(metadata), dstIP.String(), int(metadata.DstPort), "TCP", relayName, rule, opts)
	releaseFakeIP := h.pinFakeIP(rule, dstIP, sess.ID)
	defer func() {
		if releaseFakeIP != nil {
			_ = releaseFakeIP.Close()
		}
	}()
	slog.Debug("established TUN TCP session",
		"session", sess.ID,
		"target", target,
		"domain", domain,
		"dst_ip", dstIP.String(),
		"dst_port", metadata.DstPort,
		"relay", relayName,
		"rule", rule,
	)

	dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	remoteConn, err := h.selector.DialContext(dialCtx, "tcp", target)
	cancel()
	if err != nil {
		sess.SetCloseReason(fmt.Sprintf("dial relay: %v", err))
		h.sessions.CloseSession(sess.ID, session.StatusError)
		return
	}
	defer remoteConn.Close()

	sess.Relay = h.selector.ActiveName()
	sess.MarkConnected()
	h.bindSessionCloser(sess, releaseFakeIP, conn, remoteConn)
	releaseFakeIP = nil

	type copyResult struct {
		dir string
		err error
	}
	errCh := make(chan copyResult, 2)
	go func() {
		_, err := io.Copy(remoteConn, session.NewTrackedConn(conn, sess, true))
		errCh <- copyResult{dir: "upload", err: err}
	}()
	go func() {
		_, err := io.Copy(session.NewTrackedConn(conn, sess, false), remoteConn)
		errCh <- copyResult{dir: "download", err: err}
	}()

	first := <-errCh
	sess.Close()
	<-errCh

	status := session.StatusClosed
	if relayFailed(first.err) {
		status = session.StatusError
		sess.SetCloseReason(fmt.Sprintf("%s copy: %v", first.dir, first.err))
	}
	h.sessions.CloseSession(sess.ID, status)
}

func (h *handler) HandleUDPPacket(packet C.UDPPacket, metadata *C.Metadata) {
	packetAdapter := C.NewPacketAdapter(packet, metadata)
	key := packetAdapter.Key()

	sender, loaded := h.natTable.GetOrCreate(key, func() C.PacketSender {
		s := newUDPSender(h, key)
		go s.Process(nil, nil)
		return s
	})
	if !loaded {
		slog.Debug("UDP association created", "key", key)
	}
	sender.Send(packetAdapter)
}

func (h *handler) NatTable() C.NatTable {
	return h.natTable
}

func (h *handler) Providers() map[string]provider.ProxyProvider {
	return map[string]provider.ProxyProvider{}
}

func (h *handler) RuleProviders() map[string]provider.RuleProvider {
	return map[string]provider.RuleProvider{}
}

func (h *handler) RuleUpdateCallback() *utils.Callback[provider.RuleProvider] {
	return h.callbacks
}

func (h *handler) UDPStats() UDPStats {
	if h == nil {
		return UDPStats{}
	}
	return h.udp.stats()
}

func (h *handler) Close() error {
	h.closeOnce.Do(func() {
		// NAT entries are closed by their own session lifecycle.
	})
	return nil
}

func (h *handler) targetForMetadata(metadata *C.Metadata) (target string, domain string, dstIP netip.Addr, rule string, err error) {
	dstIP = metadata.DstIP.Unmap()
	host := metadata.Host
	rule = "additional-ip"

	if dstIP.IsValid() {
		if mapped, ok := h.dnsServer.FakeIPPool().LookBack(dstIP); ok {
			domain = mapped
			host = mapped
			rule = "fake-ip"
		} else if h.dnsServer.FakeIPPool().Contains(dstIP) {
			return "", "", netip.Addr{}, "", fmt.Errorf("unknown fake-ip destination %s", dstIP)
		}
	}

	if host == "" {
		if !dstIP.IsValid() {
			return "", "", netip.Addr{}, "", errors.New("destination is empty")
		}
		host = dstIP.String()
	}
	if domain == "" && rule == "fake-ip" {
		domain = host
	}

	target = net.JoinHostPort(host, fmt.Sprintf("%d", metadata.DstPort))
	return target, domain, dstIP, rule, nil
}

func (h *handler) bindSessionCloser(sess *session.Session, conns ...io.Closer) {
	var once sync.Once
	sess.SetCloseFunc(func() {
		once.Do(func() {
			for _, conn := range conns {
				if conn == nil {
					continue
				}
				_ = conn.Close()
			}
		})
	})
}

func (h *handler) sessionOpts(rule string, domain string, dstIP netip.Addr) session.SessionOpts {
	var opts session.SessionOpts
	if rule == "fake-ip" && dstIP.IsValid() {
		opts.FakeIP = dstIP.String()
		opts.DNSRequestedAt = h.dnsServer.FakeIPPool().LastLookupTime(domain)
	}
	return opts
}

func (h *handler) pinFakeIP(rule string, dstIP netip.Addr, sessionID string) io.Closer {
	if rule != "fake-ip" || !dstIP.IsValid() {
		return nil
	}
	if !h.dnsServer.FakeIPPool().Acquire(dstIP, sessionID) {
		return nil
	}
	var once sync.Once
	return closeFunc(func() {
		once.Do(func() {
			h.dnsServer.FakeIPPool().Release(dstIP, sessionID)
		})
	})
}

type udpSender struct {
	handler *handler
	key     string
	ctx     context.Context
	cancel  context.CancelFunc
	ch      chan C.PacketAdapter

	conn          net.Conn
	sess          *session.Session
	writeBack     C.WriteBackProxy
	sessionStatus session.Status
	closeReason   string
	closeOnce     sync.Once
}

func newUDPSender(h *handler, key string) *udpSender {
	ctx, cancel := context.WithCancel(context.Background())
	return &udpSender{
		handler:       h,
		key:           key,
		ctx:           ctx,
		cancel:        cancel,
		ch:            make(chan C.PacketAdapter, udpPacketQueueSize),
		sessionStatus: session.StatusClosed,
	}
}

func (s *udpSender) Process(_ C.PacketConn, _ C.WriteBackProxy) {
	defer s.cleanup()

	for {
		select {
		case <-s.ctx.Done():
			return
		case packet := <-s.ch:
			if packet == nil {
				return
			}
			if err := s.handlePacket(packet); err != nil {
				s.sessionStatus = session.StatusError
				s.closeReason = fmt.Sprintf("handle packet: %v", err)
				return
			}
		}
	}
}

func (s *udpSender) Send(packet C.PacketAdapter) {
	select {
	case <-s.ctx.Done():
		s.dropPacket(packet, udpDropClosed)
		return
	default:
	}

	select {
	case s.ch <- packet:
		s.recordEnqueued()
		return
	default:
	}

	timer := time.NewTimer(udpPacketEnqueueTimeout)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		s.dropPacket(packet, udpDropClosed)
	case s.ch <- packet:
		s.recordEnqueued()
	case <-timer.C:
		s.dropPacket(packet, udpDropQueueFull)
	}
}

func (s *udpSender) Close() {
	s.cancel()
	s.dropPending()
}

func (s *udpSender) DoSniff(*C.Metadata) error { return nil }

func (s *udpSender) AddMapping(*C.Metadata, *C.Metadata) {}

func (s *udpSender) RestoreReadFrom(addr netip.Addr) netip.Addr { return addr }

func (s *udpSender) handlePacket(packet C.PacketAdapter) error {
	if s.writeBack == nil {
		s.writeBack = nat.NewWriteBackProxy(packet)
	} else {
		s.writeBack.UpdateWriteBack(packet)
	}

	if s.conn == nil {
		if err := s.init(packet.Metadata()); err != nil {
			packet.Drop()
			return err
		}
	}

	_ = s.conn.SetDeadline(time.Now().Add(udpAssociationTimeout))

	n, err := s.conn.Write(packet.Data())
	packet.Drop()
	if n > 0 && s.sess != nil {
		s.sess.RecordUpload(n)
	}
	if err != nil {
		return err
	}
	return nil
}

func (s *udpSender) init(metadata *C.Metadata) error {
	target, domain, dstIP, rule, err := s.handler.targetForMetadata(metadata)
	if err != nil {
		return err
	}

	relayName := s.handler.selector.ActiveName()
	opts := s.handler.sessionOpts(rule, domain, dstIP)
	s.sess = s.handler.sessions.NewSession(domain, sourceAddress(metadata), dstIP.String(), int(metadata.DstPort), "UDP", relayName, rule, opts)
	releaseFakeIP := s.handler.pinFakeIP(rule, dstIP, s.sess.ID)
	defer func() {
		if releaseFakeIP != nil {
			_ = releaseFakeIP.Close()
		}
	}()
	slog.Debug("established TUN UDP session",
		"session", s.sess.ID,
		"target", target,
		"domain", domain,
		"dst_ip", dstIP.String(),
		"dst_port", metadata.DstPort,
		"relay", relayName,
		"rule", rule,
		"nat_key", s.key,
	)

	dialCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	conn, err := s.handler.selector.DialContext(dialCtx, "udp", target)
	cancel()
	if err != nil {
		s.sess.SetCloseReason(fmt.Sprintf("dial relay: %v", err))
		s.handler.sessions.CloseSession(s.sess.ID, session.StatusError)
		s.sess = nil
		return err
	}

	s.conn = conn
	s.sess.Relay = s.handler.selector.ActiveName()
	s.sess.MarkConnected()
	s.handler.bindSessionCloser(s.sess, releaseFakeIP, conn, closeFunc(s.cancel))
	releaseFakeIP = nil
	go s.readBack()
	return nil
}

func (s *udpSender) readBack() {
	buf := make([]byte, 64*1024)
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(udpAssociationTimeout))
		n, err := s.conn.Read(buf)
		if n > 0 {
			if s.sess != nil {
				s.sess.RecordDownload(n)
			}
			if s.writeBack != nil {
				if _, writeErr := s.writeBack.WriteBack(buf[:n], nil); writeErr != nil {
					s.sessionStatus = session.StatusError
					s.closeReason = fmt.Sprintf("write back: %v", writeErr)
					s.Close()
					return
				}
			}
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				s.sessionStatus = session.StatusClosed
			} else if relayFailed(err) {
				s.sessionStatus = session.StatusError
				s.closeReason = fmt.Sprintf("relay read: %v", err)
			}
			s.Close()
			return
		}
	}
}

func (s *udpSender) cleanup() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.dropPending()
		s.handler.natTable.Delete(s.key)
		if s.conn != nil {
			_ = s.conn.Close()
		}
		if s.sess != nil {
			if s.closeReason != "" {
				s.sess.SetCloseReason(s.closeReason)
			}
			s.handler.sessions.CloseSession(s.sess.ID, s.sessionStatus)
		}
	})
}

func (s *udpSender) dropPending() {
	for {
		select {
		case packet := <-s.ch:
			s.dropPacket(packet, udpDropPending)
		default:
			return
		}
	}
}

func (s *udpSender) recordEnqueued() {
	if s.handler != nil {
		s.handler.udp.packetsEnqueued.Add(1)
	}
}

type udpDropReason int

const (
	udpDropQueueFull udpDropReason = iota
	udpDropClosed
	udpDropPending
)

func (s *udpSender) dropPacket(packet C.PacketAdapter, reason udpDropReason) {
	if packet != nil {
		packet.Drop()
	}
	if s.handler == nil {
		return
	}
	switch reason {
	case udpDropQueueFull:
		s.handler.udp.queueFullDrops.Add(1)
	case udpDropClosed:
		s.handler.udp.closedDrops.Add(1)
	case udpDropPending:
		s.handler.udp.pendingDrops.Add(1)
	}
}

func (c *udpCounters) stats() UDPStats {
	queueFullDrops := c.queueFullDrops.Load()
	closedDrops := c.closedDrops.Load()
	pendingDrops := c.pendingDrops.Load()
	return UDPStats{
		PacketsEnqueued: c.packetsEnqueued.Load(),
		PacketsDropped:  queueFullDrops + closedDrops + pendingDrops,
		QueueFullDrops:  queueFullDrops,
		ClosedDrops:     closedDrops,
		PendingDrops:    pendingDrops,
	}
}

type closeFunc func()

func (f closeFunc) Close() error {
	f()
	return nil
}

func relayFailed(err error) bool {
	if err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) {
		return false
	}
	return true
}

func sourceAddress(metadata *C.Metadata) string {
	if metadata == nil || !metadata.SourceAddrPort().IsValid() {
		return ""
	}
	return metadata.SourceAddress()
}
