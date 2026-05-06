package relay

import (
	"errors"
	"log/slog"

	"github.com/punchproxy/punch/internal/config"
	"github.com/punchproxy/punch/internal/eventbus"
)

func (s *Selector) activeNameLocked() string {
	if len(s.groups) == 0 {
		return "DIRECT"
	}
	g := s.groups[s.activeUsableGroupIndexLocked()]
	d := g.dialers[s.activeDialerIndexLocked(g)]
	return s.displayName(g.name, d.Name())
}

func (s *Selector) publishRelayChangeLocked(prev string) {
	next := s.activeNameLocked()
	if prev == next || s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayChange, Data: next})
}

func (s *Selector) publishRelayChange(prev string) {
	next := s.ActiveName()
	if prev == next || s.bus == nil {
		return
	}
	s.bus.Publish(eventbus.Event{Type: eventbus.EventRelayChange, Data: next})
}

func (s *Selector) healthKey(group, relay string) string {
	return group + "\x00" + relay
}

func (s *Selector) resetSelectedCheckFailuresLocked() {
	s.selectedCheckFailures = 0
	s.selectedCheckFailureKey = ""
}

func (s *Selector) displayName(group, relay string) string {
	return group + " / " + relay
}

func (s *Selector) activeGroupIndexLocked() int {
	idx := int(s.active.Load())
	if idx < 0 || idx >= len(s.groups) {
		return 0
	}
	return idx
}

func (s *Selector) activeUsableGroupIndexLocked() int {
	if len(s.groups) == 0 {
		return 0
	}
	idx := s.activeGroupIndexLocked()
	if len(s.groups[idx].dialers) > 0 {
		return idx
	}
	for i, g := range s.groups {
		if len(g.dialers) > 0 {
			return i
		}
	}
	return 0
}

func (s *Selector) activeDialerIndexLocked(g *group) int {
	idx := int(g.active.Load())
	if idx < 0 || idx >= len(g.dialers) {
		return 0
	}
	return idx
}

func normalizeSelectMode(mode string) string {
	switch mode {
	case "auto", "manual":
		return mode
	default:
		return ""
	}
}

func displaySelectMode(mode string) string {
	if mode == "auto" {
		return "auto"
	}
	return "manual"
}

func (s *Selector) snapshotSelectionsLocked() config.RelaySelections {
	state := config.RelaySelections{GroupRelay: make(map[string]string)}
	if len(s.groups) > 0 {
		state.ActiveGroup = s.groups[s.activeUsableGroupIndexLocked()].name
	}
	for _, g := range s.groups {
		if len(g.dialers) == 0 {
			continue
		}
		state.GroupRelay[g.name] = g.dialers[s.activeDialerIndexLocked(g)].Name()
	}
	return state
}

func (s *Selector) loadSelections() config.RelaySelections {
	state, err := config.LoadRelaySelections(s.store)
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("load relay selections failed", "error", err)
		}
		return config.RelaySelections{GroupRelay: make(map[string]string)}
	}
	if state.GroupRelay == nil {
		state.GroupRelay = make(map[string]string)
	}
	return state
}

func (s *Selector) restoreSelections(state config.RelaySelections) {
	for gi, g := range s.groups {
		if len(g.dialers) == 0 {
			continue
		}
		if state.ActiveGroup == g.name {
			s.active.Store(int32(gi))
			slog.Debug("restored relay group selection", "group", g.name)
		}
		if relayName := state.GroupRelay[g.name]; relayName != "" {
			for di, d := range g.dialers {
				if d.Name() == relayName {
					g.active.Store(int32(di))
					slog.Debug("restored relay selection", "group", g.name, "relay", d.Name())
					break
				}
			}
		}
	}
}

func (s *Selector) saveSelectionsLocked() {
	state := config.RelaySelections{GroupRelay: make(map[string]string)}
	if len(s.groups) > 0 {
		state.ActiveGroup = s.groups[s.activeUsableGroupIndexLocked()].name
	}
	for _, g := range s.groups {
		if len(g.dialers) == 0 {
			continue
		}
		state.GroupRelay[g.name] = g.dialers[s.activeDialerIndexLocked(g)].Name()
	}
	if err := config.SaveRelaySelections(s.store, state); err != nil {
		slog.Warn("save relay selections failed", "error", err)
	}
}

func (s *Selector) hasUsableGroup() bool {
	for _, g := range s.groups {
		if len(g.dialers) > 0 {
			return true
		}
	}
	return false
}
