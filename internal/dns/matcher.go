package dns

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"

	"github.com/yl2chen/cidranger"
)

type SourceOpener interface {
	Open(source string) (io.ReadCloser, error)
}

// IPSet holds a sorted list of CIDR prefixes for fast IP lookup.
type IPSet struct {
	prefixes []prefixRule
	ranger   cidranger.Ranger
	sorted   bool
}

type prefixRule struct {
	prefix netip.Prefix
	source string
}

type rangerEntry struct {
	network net.IPNet
	source  string
}

func (e rangerEntry) Network() net.IPNet {
	return e.network
}

func NewIPSet() *IPSet {
	return &IPSet{ranger: cidranger.NewPCTrieRanger()}
}

func (s *IPSet) Add(prefix netip.Prefix) {
	s.AddWithSource(prefix, prefix.Masked().String())
}

func (s *IPSet) AddWithSource(prefix netip.Prefix, source string) {
	prefix = prefix.Masked()
	s.ensureRanger()
	_ = s.ranger.Insert(rangerEntry{network: prefixToIPNet(prefix), source: source})
	s.prefixes = append(s.prefixes, prefixRule{prefix: prefix, source: source})
	s.sorted = false
}

func (s *IPSet) Sort() {
	sort.Slice(s.prefixes, func(i, j int) bool {
		ai, aj := s.prefixes[i].prefix.Addr(), s.prefixes[j].prefix.Addr()
		if ai == aj {
			return s.prefixes[i].prefix.Bits() < s.prefixes[j].prefix.Bits()
		}
		return ai.Less(aj)
	})
	s.sorted = true
}

func (s *IPSet) Prefixes() []netip.Prefix {
	if !s.sorted {
		s.Sort()
	}
	result := make([]netip.Prefix, len(s.prefixes))
	for i, prefix := range s.prefixes {
		result[i] = prefix.prefix
	}
	return result
}

// Contains checks if the given IP falls within any prefix in the set.
func (s *IPSet) Contains(ip netip.Addr) bool {
	return s.ContainsSource(ip) != ""
}

func (s *IPSet) ContainsSource(ip netip.Addr) string {
	if s == nil || s.ranger == nil {
		return ""
	}
	entries, err := s.ranger.ContainingNetworks(addrToIP(ip))
	if err != nil || len(entries) == 0 {
		return ""
	}
	if entry, ok := entries[len(entries)-1].(rangerEntry); ok {
		return entry.source
	}
	if entry, ok := entries[len(entries)-1].(*rangerEntry); ok {
		return entry.source
	}
	return ""
}

func prefixToIPNet(prefix netip.Prefix) net.IPNet {
	bits := 128
	if prefix.Addr().Is4() {
		bits = 32
	}
	return net.IPNet{
		IP:   addrToIP(prefix.Addr()),
		Mask: net.CIDRMask(prefix.Bits(), bits),
	}
}

func addrToIP(addr netip.Addr) net.IP {
	if addr.Is4() {
		a := addr.As4()
		return net.IPv4(a[0], a[1], a[2], a[3])
	}
	a := addr.As16()
	return net.IP(a[:])
}

func parseCIDRLine(line string) (netip.Prefix, bool) {
	prefix, err := netip.ParsePrefix(line)
	if err == nil {
		return prefix, true
	}
	ip, err := netip.ParseAddr(line)
	if err != nil {
		return netip.Prefix{}, false
	}
	bits := 32
	if ip.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(ip, bits), true
}

func stripCIDRComment(line string) string {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimSpace(line)
}

func (s *IPSet) ensureRanger() {
	if s.ranger != nil {
		return
	}
	s.ranger = cidranger.NewPCTrieRanger()
	for _, prefix := range s.prefixes {
		_ = s.ranger.Insert(rangerEntry{
			network: prefixToIPNet(prefix.prefix),
			source:  prefix.source,
		})
	}
}

// LoadIPSet loads CIDR entries from a file or URL.
func LoadIPSet(source string, set *IPSet, opener SourceOpener) (int, error) {
	var reader io.ReadCloser
	var err error

	if opener != nil {
		reader, err = opener.Open(source)
		if err != nil {
			return 0, err
		}
	} else {
		reader, err = os.Open(source)
		if err != nil {
			return 0, fmt.Errorf("open %s: %w", source, err)
		}
	}
	defer reader.Close()

	count := 0
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := stripCIDRComment(scanner.Text())
		if line == "" {
			continue
		}
		for _, field := range strings.Fields(line) {
			prefix, ok := parseCIDRLine(field)
			if !ok {
				continue // skip invalid fields
			}
			set.AddWithSource(prefix, source)
			count++
		}
	}
	set.Sort()
	return count, scanner.Err()
}
