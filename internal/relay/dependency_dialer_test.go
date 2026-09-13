package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/transport/socks5"
)

func TestDialerProxyGroupValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"valid", "transit", true},
		{"empty", "", false},
		{"spaces", " ", false},
		{"leading space", " transit", false},
		{"number", 12, false},
		{"null", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, err := DialerProxyGroup(map[string]any{"name": "exit", "dialer-proxy": tc.value})
			if (err == nil) != tc.valid || (tc.valid && name != tc.value) {
				t.Fatalf("DialerProxyGroup() = %q, %v", name, err)
			}
		})
	}
	if name, err := DialerProxyGroup(nil); name != "" || err != nil {
		t.Fatalf("absent dialer-proxy = %q, %v", name, err)
	}
	for _, relayType := range []string{"direct", "dns", "reject", "rematch", "tailscale"} {
		_, err := NewDependencyDialer("exit", map[string]any{
			"name": "relay", "type": relayType, "dialer-proxy": "transit",
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "does not support dialer-proxy") {
			t.Errorf("type %s error = %v", relayType, err)
		}
	}
}

type dependencyFailDialer struct {
	*DirectDialer
	calls  atomic.Int64
	closed atomic.Bool
	mu     sync.Mutex
	addrs  []string
}

var errDependencyTestDial = errors.New("upstream sentinel")

func (d *dependencyFailDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.calls.Add(1)
	d.mu.Lock()
	d.addrs = append(d.addrs, address)
	d.mu.Unlock()
	return nil, errDependencyTestDial
}

func (d *dependencyFailDialer) Close() error {
	d.closed.Store(true)
	return nil
}

func TestDependencyBindingPinsUpstreamAndPreservesDialerOnDNSRefresh(t *testing.T) {
	var resolutions atomic.Int64
	dialer, err := NewDependencyDialer("exit", map[string]any{
		"name": "exit-1", "type": "socks5", "server": "exit.example", "port": 1080,
		"dialer-proxy": "transit", "udp": true,
	}, func(_ context.Context, group, host string) ([]netip.Addr, time.Time, error) {
		if group != "exit" || host != "exit.example" {
			t.Errorf("resolver group/host = %q/%q", group, host)
		}
		index := resolutions.Add(1)
		return []netip.Addr{netip.AddrFrom4([4]byte{192, 0, 2, byte(index)})}, time.Now().Add(time.Minute), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dialer.Close() })
	dependency := dialer.(dependentDialer)
	if _, err := dependency.DialContext(context.Background(), "tcp", "192.0.2.10:80"); err == nil {
		t.Fatal("unbound relay dial succeeded")
	}
	first := &dependencyFailDialer{DirectDialer: NewDirectDialer(nil)}
	bound := dependency.BindDependency(first, "path-1")
	if resolutions.Load() != 0 || first.calls.Load() != 0 {
		t.Fatal("binding performed network work")
	}
	if bound != dependency.BindDependency(first, "path-1") {
		t.Fatal("same path did not reuse its adapter")
	}
	speculative := &dependencyFailDialer{DirectDialer: NewDirectDialer(nil)}
	probe := dialer.(*dependencyDialer).BindDependencyForCheck(speculative, "speculative")
	if probe == bound || resolutions.Load() != 0 {
		t.Fatal("speculative binding reused the active adapter or resolved DNS")
	}
	if _, err := probe.DialContext(context.Background(), "tcp", "192.0.2.10:80"); !errors.Is(err, errDependencyTestDial) {
		t.Fatalf("speculative probe error = %v", err)
	}
	if bound != dependency.BindDependency(first, "path-1") || first.closed.Load() {
		t.Fatal("speculative probe evicted or closed the active adapter")
	}
	resolutions.Store(0)
	for i := 0; i < 2; i++ {
		_, err := bound.DialContext(context.Background(), "tcp", "192.0.2.10:80")
		if !errors.Is(err, errDependencyTestDial) {
			t.Fatalf("dial error = %v", err)
		}
		lazy := bound.(*boundDependencyDialer).Dialer.(*LazyRelayDialer)
		lazy.mu.Lock()
		lazy.expiresAt = time.Now().Add(-time.Second)
		lazy.mu.Unlock()
	}
	if first.calls.Load() != 2 || resolutions.Load() != 2 {
		t.Fatalf("dial/DNS calls = %d/%d", first.calls.Load(), resolutions.Load())
	}
	first.mu.Lock()
	if first.addrs[0] != "192.0.2.1:1080" || first.addrs[1] != "192.0.2.2:1080" {
		t.Errorf("upstream endpoints = %v", first.addrs)
	}
	first.mu.Unlock()
	second := &dependencyFailDialer{DirectDialer: NewDirectDialer(nil)}
	next := dependency.BindDependency(second, "path-2")
	if next == bound || first.closed.Load() {
		t.Fatal("path replacement reused or closed the old adapter")
	}
	_, _ = next.DialContext(context.Background(), "tcp", "192.0.2.10:80")
	_, _ = bound.DialContext(context.Background(), "tcp", "192.0.2.10:80")
	if first.calls.Load() != 3 || second.calls.Load() != 1 {
		t.Fatalf("old/new upstream calls = %d/%d", first.calls.Load(), second.calls.Load())
	}
}

func TestGroupTransportRejectsMissingPacketSupport(t *testing.T) {
	bridge := &groupTransportDialer{group: "transit", upstream: &closeTrackingDialer{}}
	_, err := bridge.ListenPacket(context.Background(), "udp", "", netip.MustParseAddrPort("192.0.2.1:443"))
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("packet error = %v", err)
	}
	_, err = bridge.ListenPacket(context.Background(), "udp", "", netip.AddrPort{})
	if err == nil || !strings.Contains(err.Error(), "remote UDP endpoint") {
		t.Fatalf("missing endpoint error = %v", err)
	}
}

func TestDependencyDialerSOCKSChainTCPAndUDP(t *testing.T) {
	transitServer := newDependencySOCKSServer(t)
	exitServer := newDependencySOCKSServer(t)
	transit, err := NewLazyRelayDialer("transit", transitServer.mapping("transit-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transit.Close() })
	mapping := exitServer.mapping("exit-1")
	mapping["dialer-proxy"] = "transit"
	configuration, err := NewDependencyDialer("exit", mapping, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configuration.Close() })
	bound := configuration.(dependentDialer).BindDependency(transit, "exit-1/transit-1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The test SOCKS servers echo connections addressed to their own listener.
	conn, err := bound.DialContext(ctx, "tcp", exitServer.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	assertDependencyEcho(t, conn)
	if got := transitServer.nextTCP(t); got != exitServer.addr {
		t.Fatalf("transit TCP target = %s, want %s", got, exitServer.addr)
	}
	if got := exitServer.nextTCP(t); got != exitServer.addr {
		t.Fatalf("exit TCP target = %s, want %s", got, exitServer.addr)
	}
	pc, err := bound.(PacketDialer).ListenPacketContext(ctx, "udp", exitServer.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	_ = pc.SetDeadline(time.Now().Add(5 * time.Second))
	remote, _ := net.ResolveUDPAddr("udp", exitServer.addr)
	if _, err := pc.WriteTo([]byte("packet echo"), remote); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, from, err := pc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "packet echo" || from.String() != remote.String() {
		t.Fatalf("UDP echo = %q, %v, %v", buf[:n], from, err)
	}
	if got := transitServer.nextUDP(t); got != exitServer.addr {
		t.Fatalf("transit UDP target = %s, want %s", got, exitServer.addr)
	}
	if got := exitServer.nextUDP(t); got != exitServer.addr {
		t.Fatalf("exit UDP target = %s, want %s", got, exitServer.addr)
	}
	// Replacing the selected upstream builds a new adapter for future dials.
	// Existing streams continue to use the adapter that opened them.
	failed := &dependencyFailDialer{DirectDialer: NewDirectDialer(nil)}
	next := configuration.(dependentDialer).BindDependency(failed, "exit-1/transit-2")
	if _, err := next.DialContext(ctx, "tcp", exitServer.addr); !errors.Is(err, errDependencyTestDial) {
		t.Fatalf("new path dial error = %v", err)
	}
	assertDependencyEcho(t, conn)
}

func TestRelayMetadataUsesIPDestination(t *testing.T) {
	for _, address := range []string{"192.0.2.1:443", "[2001:db8::1]:443"} {
		metadata, err := relayMetadata("udp", address)
		if err != nil || !metadata.DstIP.IsValid() || metadata.Host != "" || metadata.RemoteAddress() != address {
			t.Fatalf("metadata(%q) = %v, %v", address, metadata, err)
		}
	}
	metadata, err := relayMetadata("tcp", "exit.example:443")
	if err != nil || metadata.Host != "exit.example" || metadata.DstIP.IsValid() {
		t.Fatalf("domain metadata = %v, %v", metadata, err)
	}
	if _, err := relayMetadata("tcp", "192.0.2.1:65536"); err == nil {
		t.Fatal("accepted a port outside the uint16 range")
	}
}

func TestDirectPacketDialerUsesConfiguredRoute(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var calls atomic.Int64
	direct := NewDirectDialer(func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	})
	pc, err := direct.ListenPacketContext(context.Background(), "udp", server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if calls.Load() != 1 {
		t.Fatalf("configured direct dial calls = %d", calls.Load())
	}
	_ = pc.SetDeadline(time.Now().Add(time.Second))
	_ = server.SetDeadline(time.Now().Add(time.Second))
	if _, err := pc.WriteTo([]byte("direct"), server.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	n, from, err := server.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "direct" {
		t.Fatalf("server received %q, %v", buf[:n], err)
	}
	_, _ = server.WriteTo(buf[:n], from)
	n, from, err = pc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "direct" || from.String() != server.LocalAddr().String() {
		t.Fatalf("client received %q, %v, %v", buf[:n], from, err)
	}
	if _, err := pc.WriteTo(buf[:n], &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}); err == nil {
		t.Fatal("connected packet socket accepted a different destination")
	}
}

func assertDependencyEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	const payload = "stream echo"
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != payload {
		t.Fatalf("TCP echo = %q, %v", buf, err)
	}
}

type dependencySOCKSServer struct {
	addr       string
	tcpTargets chan string
	udpTargets chan string
}

func (s *dependencySOCKSServer) mapping(name string) map[string]any {
	host, port, _ := net.SplitHostPort(s.addr)
	return map[string]any{"name": name, "type": "socks5", "server": host, "port": port, "udp": true}
}

func (s *dependencySOCKSServer) nextTCP(t *testing.T) string {
	t.Helper()
	return nextDependencyTarget(t, s.tcpTargets)
}

func (s *dependencySOCKSServer) nextUDP(t *testing.T) string {
	t.Helper()
	return nextDependencyTarget(t, s.udpTargets)
}

func nextDependencyTarget(t *testing.T, targets <-chan string) string {
	t.Helper()
	select {
	case target := <-targets:
		return target
	case <-time.After(time.Second):
		t.Fatal("SOCKS server did not observe a target")
		return ""
	}
}

func newDependencySOCKSServer(t *testing.T) *dependencySOCKSServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := net.ListenPacket("udp4", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &dependencySOCKSServer{addr: listener.Addr().String(), tcpTargets: make(chan string, 8), udpTargets: make(chan string, 8)}
	var mu sync.Mutex
	var stopped bool
	connections := make(map[net.Conn]struct{})
	t.Cleanup(func() {
		listener.Close()
		packet.Close()
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		for conn := range connections {
			conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if stopped {
				conn.Close()
				mu.Unlock()
				return
			}
			connections[conn] = struct{}{}
			mu.Unlock()
			go func() {
				defer func() {
					conn.Close()
					mu.Lock()
					delete(connections, conn)
					mu.Unlock()
				}()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				target, command, _, err := socks5.ServerHandshake(conn, nil)
				if err != nil {
					return
				}
				if command == socks5.CmdUDPAssociate {
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				server.tcpTargets <- target.String()
				if target.String() == server.addr {
					_, _ = io.Copy(conn, conn)
					return
				}
				upstream, err := net.DialTimeout("tcp", target.String(), time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				_ = upstream.SetDeadline(time.Now().Add(5 * time.Second))
				go func() { _, _ = io.Copy(upstream, conn) }()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	go func() {
		buf := make([]byte, 65536)
		for {
			n, from, err := packet.ReadFrom(buf)
			if err != nil {
				return
			}
			target, payload, err := socks5.DecodeUDPPacket(buf[:n])
			if err != nil {
				return
			}
			server.udpTargets <- target.String()
			if target.String() == server.addr {
				reply, _ := socks5.EncodeUDPPacket(target, payload)
				_, _ = packet.WriteTo(reply, from)
				continue
			}
			host, port, _ := net.SplitHostPort(target.String())
			portNum, _ := strconv.Atoi(port)
			upstream, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(host), Port: portNum})
			if err != nil {
				return
			}
			_ = upstream.SetDeadline(time.Now().Add(time.Second))
			_, err = upstream.Write(payload)
			if err != nil {
				upstream.Close()
				return
			}
			replyBuf := make([]byte, 65536)
			n, err = upstream.Read(replyBuf)
			upstream.Close()
			if err != nil {
				return
			}
			reply, _ := socks5.EncodeUDPPacket(target, replyBuf[:n])
			_, _ = packet.WriteTo(reply, from)
		}
	}()
	return server
}
