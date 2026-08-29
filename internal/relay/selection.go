package relay

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
)

func (s *Selector) Active() Dialer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.groups) == 0 {
		return &DirectDialer{}
	}
	g := s.groups[s.activeUsableGroupIndexLocked()]
	idx := s.activeDialerIndexLocked(g)
	return g.dialers[idx]
}

func (s *Selector) ActiveName() string {
	// Keep display/status reads passive. Expired relay hostnames are refreshed on
	// actual dial paths and explicit health checks, not on API reads.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.groups) == 0 {
		return "DIRECT"
	}
	g := s.groups[s.activeUsableGroupIndexLocked()]
	d := g.dialers[s.activeDialerIndexLocked(g)]
	return s.displayName(g.name, d.Name())
}

func (s *Selector) Select(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prevActive := s.activeNameLocked()

	for gi, g := range s.groups {
		if len(g.dialers) == 0 {
			continue
		}
		if g.name == name {
			s.active.Store(int32(gi))
			s.resetSelectedCheckFailuresLocked()
			s.saveSelectionsLocked()
			slog.Debug("manually selected relay group", "group", g.name)
			s.publishRelayChangeLocked(prevActive)
			return nil
		}
		for di, d := range g.dialers {
			if s.displayName(g.name, d.Name()) == name || d.Name() == name {
				g.active.Store(int32(di))
				s.active.Store(int32(gi))
				s.resetSelectedCheckFailuresLocked()
				s.saveSelectionsLocked()
				slog.Debug("manually selected relay", "group", g.name, "relay", d.Name())
				s.publishRelayChangeLocked(prevActive)
				return nil
			}
		}
	}
	return fmt.Errorf("relay %q not found", name)
}

func (s *Selector) SelectManualRelay(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prevActive := s.activeNameLocked()

	type match struct {
		groupIdx int
		relayIdx int
		group    *group
		relay    Dialer
	}
	var matches []match
	var autoGroups []string
	for gi, g := range s.groups {
		for di, d := range g.dialers {
			if d.Name() != name && s.displayName(g.name, d.Name()) != name {
				continue
			}
			if g.mode == "auto" {
				autoGroups = append(autoGroups, g.name)
				continue
			}
			matches = append(matches, match{groupIdx: gi, relayIdx: di, group: g, relay: d})
		}
	}
	if len(matches) == 0 {
		if len(autoGroups) > 0 {
			return "", fmt.Errorf("%w: %s", ErrRelaySelectionAutoGroup, strings.Join(autoGroups, ","))
		}
		return "", fmt.Errorf("relay %q not found", name)
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, s.displayName(m.group.name, m.relay.Name()))
		}
		return "", fmt.Errorf("%w: %s", ErrRelaySelectionAmbiguous, strings.Join(names, ", "))
	}
	selected := matches[0]
	selected.group.active.Store(int32(selected.relayIdx))
	s.active.Store(int32(selected.groupIdx))
	s.resetSelectedCheckFailuresLocked()
	s.saveSelectionsLocked()
	slog.Debug("manually selected relay", "group", selected.group.name, "relay", selected.relay.Name())
	s.publishRelayChangeLocked(prevActive)
	return s.displayName(selected.group.name, selected.relay.Name()), nil
}

func (s *Selector) SelectManualGroup(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == "auto" {
		return "", ErrGroupSelectionAutoMode
	}
	prevActive := s.activeNameLocked()
	for gi, g := range s.groups {
		if len(g.dialers) == 0 || g.name != name {
			continue
		}
		s.active.Store(int32(gi))
		s.resetSelectedCheckFailuresLocked()
		s.saveSelectionsLocked()
		slog.Debug("manually selected relay group", "group", g.name)
		s.publishRelayChangeLocked(prevActive)
		return g.name, nil
	}
	return "", fmt.Errorf("relay group %q not found", name)
}

func (s *Selector) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, _, err := s.DialContextRelay(ctx, network, address)
	return conn, err
}

// DialContextRelay dials like DialContext and additionally reports the display
// name of the relay that actually carried the dial. Callers that want to label
// a connection must use this rather than reading ActiveName() afterwards: the
// selector can switch relays while the dial is in flight, which would attribute
// the connection to the wrong relay.
func (s *Selector) DialContextRelay(ctx context.Context, network, address string) (net.Conn, string, error) {
	d, name := s.activeSelection()
	conn, err := d.DialContext(ctx, network, address)
	if err != nil {
		return nil, name, err
	}
	return conn, name, nil
}

func (s *Selector) activeSelection() (Dialer, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.groups) == 0 {
		return &DirectDialer{}, "DIRECT"
	}
	g := s.groups[s.activeUsableGroupIndexLocked()]
	d := g.dialers[s.activeDialerIndexLocked(g)]
	return d, s.displayName(g.name, d.Name())
}
