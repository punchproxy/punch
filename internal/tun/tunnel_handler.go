package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/miekg/dns"
	pdns "github.com/punchproxy/punch/internal/dns"
	"github.com/punchproxy/punch/internal/relay"
	"github.com/punchproxy/punch/internal/session"
	singtun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing-tun/ping"
	singbuf "github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
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

	inet4Address []netip.Prefix
	inet6Address []netip.Prefix
	udp          udpCounters

	closeOnce sync.Once
}

func newHandler(dnsServer *pdns.Server, selector *relay.Selector, sessions *session.Manager, inet4Address, inet6Address []netip.Prefix) *handler {
	return &handler{
		dnsServer:    dnsServer,
		selector:     selector,
		sessions:     sessions,
		inet4Address: clonePrefixes(inet4Address),
		inet6Address: clonePrefixes(inet6Address),
	}
}

func (h *handler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var closeErr error
	defer func() {
		if onClose != nil {
			onClose(closeErr)
		}
	}()

	if isDNSDestination(destination) {
		closeErr = h.handleDNSConn(ctx, conn)
		return
	}
	closeErr = h.handleTCPConn(conn, source, destination)
}

func (h *handler) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, _ M.Socksaddr, onClose N.CloseHandlerFunc) {
	var closeErr error
	defer func() {
		if onClose != nil {
			onClose(closeErr)
		}
	}()
	defer conn.Close()

	writer := &packetWriteBack{conn: conn}
	senders := make(map[string]*udpSender)
	defer func() {
		for _, sender := range senders {
			sender.Close()
		}
	}()

	for {
		packet := singbuf.NewPacket()
		destination, err := conn.ReadPacket(packet)
		if err != nil {
			packet.Release()
			if relayFailed(err) {
				closeErr = err
			}
			return
		}

		if isDNSDestination(destination) {
			h.handleDNSPacket(ctx, writer, packet, destination)
			continue
		}

		key := destination.String()
		sender := senders[key]
		if sender == nil {
			sender = newUDPSender(h, source.String()+"->"+key, writer, source, destination)
			senders[key] = sender
			go sender.Process()
		}
		sender.Send(&udpTunPacket{buffer: packet, destination: destination})
	}
}

func (h *handler) PrepareConnection(network string, _ M.Socksaddr, destination M.Socksaddr, routeContext singtun.DirectRouteContext, timeout time.Duration) (singtun.DirectRouteDestination, error) {
	if network != N.NetworkICMP || !destination.Addr.IsValid() || h.skipPingForwarding(destination.Addr) {
		return nil, nil
	}
	directRouteDestination, err := ping.ConnectDestination(context.Background(), singLogger{}, nil, destination.Addr, routeContext, timeout)
	if err != nil {
		slog.Debug("failed to forward ICMP destination directly", "destination", destination.String(), "error", err)
		return nil, err
	}
	return directRouteDestination, nil
}

func (h *handler) UDPStats() UDPStats {
	if h == nil {
		return UDPStats{}
	}
	return h.udp.stats()
}

func (h *handler) Close() error {
	h.closeOnce.Do(func() {})
	return nil
}

func (h *handler) handleTCPConn(conn net.Conn, source M.Socksaddr, destination M.Socksaddr) error {
	defer conn.Close()

	target, domain, dstIP, rule, err := h.targetForDestination(destination)
	if err != nil {
		slog.Debug("drop invalid TCP TUN connection", "error", err)
		return err
	}

	relayName := h.selector.ActiveName()
	opts := h.sessionOpts(rule, domain, dstIP)
	sess := h.sessions.NewSession(domain, source.String(), dstIP.String(), int(destination.Port), "TCP", relayName, rule, opts)
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
		"dst_port", destination.Port,
		"relay", relayName,
		"rule", rule,
	)

	dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	dialStart := time.Now()
	remoteConn, err := h.selector.DialContext(dialCtx, "tcp", target)
	cancel()
	if err != nil {
		sess.SetCloseReason(fmt.Sprintf("dial relay: %v", err))
		h.sessions.CloseSession(sess.ID, session.StatusError)
		return err
	}
	defer remoteConn.Close()

	sess.Relay = h.selector.ActiveName()
	h.selector.RecordConnectLatency(sess.Relay, time.Since(dialStart))
	sess.MarkConnected()
	h.bindSessionCloser(sess, releaseFakeIP, conn, remoteConn)
	releaseFakeIP = nil

	type copyResult struct {
		dir string
		err error
	}
	remote := relayTaggedConn{Conn: remoteConn}
	errCh := make(chan copyResult, 2)
	go func() {
		_, err := io.Copy(remote, session.NewTrackedConn(conn, sess, true))
		errCh <- copyResult{dir: "upload", err: err}
	}()
	go func() {
		_, err := io.Copy(session.NewTrackedConn(conn, sess, false), remote)
		errCh <- copyResult{dir: "download", err: err}
	}()

	first := <-errCh
	sess.Close()
	<-errCh

	abnormal, relaySide := classifyCopyError(first.err)
	if !abnormal {
		h.sessions.CloseSession(sess.ID, session.StatusClosed)
		return nil
	}
	side := "client"
	if relaySide {
		side = "relay"
	}
	sess.SetCloseReason(fmt.Sprintf("%s copy (%s side): %v", first.dir, side, first.err))
	if relaySide {
		h.selector.ReportStreamAbort(sess.Relay)
		slog.Warn("relay aborted stream mid-transfer",
			"session", sess.ID,
			"relay", sess.Relay,
			"target", target,
			"direction", first.dir,
			"upload_bytes", sess.UploadBytes(),
			"download_bytes", sess.DownloadBytes(),
			"duration", time.Since(sess.StartTime).Round(time.Millisecond),
			"error", first.err,
		)
	}
	h.sessions.CloseSession(sess.ID, session.StatusError)
	return first.err
}

func (h *handler) targetForDestination(destination M.Socksaddr) (target string, domain string, dstIP netip.Addr, rule string, err error) {
	dstIP = destination.Addr.Unmap()
	host := destination.Fqdn
	rule = "additional-ip"

	if dstIP.IsValid() && h.dnsServer != nil {
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

	target = net.JoinHostPort(host, strconv.Itoa(int(destination.Port)))
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
	if h.dnsServer != nil && rule == "fake-ip" && dstIP.IsValid() {
		opts.FakeIP = dstIP.String()
		opts.DNSRequestedAt = h.dnsServer.FakeIPPool().LastLookupTime(domain)
	}
	return opts
}

func (h *handler) pinFakeIP(rule string, dstIP netip.Addr, sessionID string) io.Closer {
	if h.dnsServer == nil || rule != "fake-ip" || !dstIP.IsValid() {
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

func (h *handler) skipPingForwarding(addr netip.Addr) bool {
	for _, prefix := range h.inet4Address {
		if prefix.Contains(addr) {
			return true
		}
	}
	for _, prefix := range h.inet6Address {
		if prefix.Contains(addr) {
			return true
		}
	}
	return h.dnsServer != nil && h.dnsServer.FakeIPPool().Contains(addr)
}

func isDNSDestination(destination M.Socksaddr) bool {
	return destination.Port == 53
}

func (h *handler) handleDNSConn(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	dnsConn := &dns.Conn{Conn: conn}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(udpAssociationTimeout))
		query, err := dnsConn.ReadMsg()
		if err != nil {
			if relayFailed(err) {
				return err
			}
			return nil
		}
		response, serveErr := h.serveDNSMsg(ctx, query)
		_ = conn.SetWriteDeadline(time.Now().Add(udpAssociationTimeout))
		if err := dnsConn.WriteMsg(response); err != nil {
			return err
		}
		if serveErr != nil {
			slog.Debug("served TUN DNS TCP query with failure response", "error", serveErr)
		}
	}
}

func (h *handler) handleDNSPacket(ctx context.Context, writer N.PacketWriter, packet *singbuf.Buffer, destination M.Socksaddr) {
	defer packet.Release()
	query := new(dns.Msg)
	if err := query.Unpack(packet.Bytes()); err != nil {
		slog.Debug("drop invalid TUN DNS packet", "destination", destination.String(), "error", err)
		return
	}
	response, serveErr := h.serveDNSMsg(ctx, query)
	if serveErr != nil {
		slog.Debug("served TUN DNS packet with failure response", "destination", destination.String(), "error", serveErr)
	}
	payload, err := response.Pack()
	if err != nil {
		slog.Debug("failed to pack TUN DNS response", "destination", destination.String(), "error", err)
		return
	}
	buffer := singbuf.NewPacket()
	buffer.Write(payload)
	if err := writer.WritePacket(buffer, destination); err != nil {
		slog.Debug("failed to write TUN DNS response", "destination", destination.String(), "error", err)
	}
}

func (h *handler) serveDNSMsg(ctx context.Context, query *dns.Msg) (*dns.Msg, error) {
	if h.dnsServer == nil {
		response := new(dns.Msg)
		response.SetRcode(query, dns.RcodeServerFailure)
		return response, nil
	}
	response, err := h.dnsServer.ServeMsg(ctx, query)
	if err != nil {
		response = new(dns.Msg)
		response.SetRcode(query, dns.RcodeServerFailure)
		return response, err
	}
	return response, nil
}

type packetWriteBack struct {
	mu   sync.Mutex
	conn N.PacketConn
}

func (w *packetWriteBack) WritePacket(buffer *singbuf.Buffer, destination M.Socksaddr) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WritePacket(buffer, destination)
}

type udpTunPacket struct {
	buffer      *singbuf.Buffer
	destination M.Socksaddr
}

type udpSender struct {
	handler     *handler
	key         string
	writer      N.PacketWriter
	source      M.Socksaddr
	destination M.Socksaddr
	ctx         context.Context
	cancel      context.CancelFunc
	ch          chan *udpTunPacket

	conn          net.Conn
	sess          *session.Session
	sessionStatus session.Status
	closeReason   string
	closeOnce     sync.Once
}

func newUDPSender(h *handler, key string, writer N.PacketWriter, source M.Socksaddr, destination M.Socksaddr) *udpSender {
	ctx, cancel := context.WithCancel(context.Background())
	return &udpSender{
		handler:       h,
		key:           key,
		writer:        writer,
		source:        source,
		destination:   destination,
		ctx:           ctx,
		cancel:        cancel,
		ch:            make(chan *udpTunPacket, udpPacketQueueSize),
		sessionStatus: session.StatusClosed,
	}
}

func (s *udpSender) Process() {
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

func (s *udpSender) Send(packet *udpTunPacket) {
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

func (s *udpSender) handlePacket(packet *udpTunPacket) error {
	defer packet.buffer.Release()

	if s.conn == nil {
		if err := s.init(); err != nil {
			return err
		}
	}

	_ = s.conn.SetDeadline(time.Now().Add(udpAssociationTimeout))
	n, err := s.conn.Write(packet.buffer.Bytes())
	if n > 0 && s.sess != nil {
		s.sess.RecordUpload(n)
	}
	return err
}

func (s *udpSender) init() error {
	target, domain, dstIP, rule, err := s.handler.targetForDestination(s.destination)
	if err != nil {
		return err
	}

	relayName := s.handler.selector.ActiveName()
	opts := s.handler.sessionOpts(rule, domain, dstIP)
	s.sess = s.handler.sessions.NewSession(domain, s.source.String(), dstIP.String(), int(s.destination.Port), "UDP", relayName, rule, opts)
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
		"dst_port", s.destination.Port,
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
			if s.writer != nil {
				buffer := singbuf.NewPacket()
				buffer.Write(buf[:n])
				if writeErr := s.writer.WritePacket(buffer, s.destination); writeErr != nil {
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
				if s.sess != nil {
					s.handler.selector.ReportStreamAbort(s.sess.Relay)
					slog.Warn("relay aborted UDP flow",
						"session", s.sess.ID,
						"relay", s.sess.Relay,
						"destination", s.destination.String(),
						"error", err,
					)
				}
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

func (s *udpSender) dropPacket(packet *udpTunPacket, reason udpDropReason) {
	if packet != nil && packet.buffer != nil {
		packet.buffer.Release()
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
