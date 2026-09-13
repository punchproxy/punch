package relay

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"time"

	"github.com/punchproxy/punch/internal/config"
)

// RelayHop identifies a relay in a path, ordered from the first hop to the exit.
type RelayHop struct {
	Group string `json:"group"`
	Relay string `json:"relay"`
}

type unavailableDialer struct {
	Dialer
	err error
}

func (d *unavailableDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, d.err
}
func (d *unavailableDialer) SupportUDP() bool { return false }
func (d *unavailableDialer) Close() error     { return nil }

func dependencyName(d Dialer) string {
	if dep, ok := d.(interface{ DependencyGroup() string }); ok {
		return dep.DependencyGroup()
	}
	return ""
}

// ValidateConfig checks the complete prospective graph before it is persisted.
func (s *Selector) ValidateConfig(cfg config.Relay) error {
	_, _, err := s.buildGroups(cfg)
	return err
}

func validateDependencyGraph(groups []*group) error {
	byName := make(map[string]*group, len(groups))
	for _, g := range groups {
		if _, exists := byName[g.name]; exists {
			return fmt.Errorf("duplicate relay group %q", g.name)
		}
		byName[g.name] = g
	}
	state := make(map[string]int, len(groups))
	var visit func(string, []string) error
	visit = func(name string, path []string) error {
		if state[name] == 1 {
			return fmt.Errorf("dialer-proxy cycle: %s", strings.Join(append(path, name), " -> "))
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, d := range byName[name].dialers {
			dep := dependencyName(d)
			if dep == "" {
				continue
			}
			if byName[dep] == nil {
				return fmt.Errorf("relay %q in group %q: dialer-proxy group %q not found", d.Name(), name, dep)
			}
			if err := visit(dep, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for _, g := range groups {
		if err := visit(g.name, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *Selector) groupByNameLocked(name string) *group {
	for _, g := range s.groups {
		if g.name == name {
			return g
		}
	}
	return nil
}

func (s *Selector) exitEligibleLocked(g *group) bool {
	return g.name == directGroupName || s.groupCfgs[g.name].ExitAllowed()
}

// Preserve unchanged relay instances so settings edits do not rotate live pools.
func (s *Selector) assignGenerationsLocked(groups []*group) {
	for _, g := range groups {
		old := s.groupByNameLocked(g.name)
		s.nextGeneration++
		g.generation = s.nextGeneration
		g.relayGenerations = make(map[string]uint64, len(g.dialers))
		for i, d := range g.dialers {
			reused := false
			if old != nil {
				for _, previous := range old.dialers {
					if previous.Name() == d.Name() && previous.Type() == d.Type() && previous.Addr() == d.Addr() && reflect.DeepEqual(old.specs[d.Name()], g.specs[d.Name()]) {
						g.dialers[i] = previous
						g.relayGenerations[d.Name()] = relayGeneration(old, previous)
						reused = true
						break
					}
				}
			}
			if !reused {
				s.nextGeneration++
				g.relayGenerations[d.Name()] = s.nextGeneration
			}
		}
	}
}

func relayGeneration(g *group, d Dialer) uint64 {
	if generation, ok := g.relayGenerations[d.Name()]; ok {
		return generation
	}
	return g.generation
}

// resolvePathLocked only snapshots selection; it never resolves DNS or dials.
func (s *Selector) resolvePathLocked(g *group, d Dialer, seen map[string]bool, bind bool) (Dialer, []RelayHop, string) {
	return s.resolvePathWithChoicesLocked(g, d, seen, bind, nil)
}

func (s *Selector) resolvePathWithChoicesLocked(g *group, d Dialer, seen map[string]bool, bind bool, choices map[string]int) (Dialer, []RelayHop, string) {
	hop := RelayHop{Group: g.name, Relay: d.Name()}
	key := fmt.Sprintf("%q:%d:%q", g.name, relayGeneration(g, d), d.Name())
	depName := dependencyName(d)
	if depName == "" {
		return d, []RelayHop{hop}, key
	}
	if seen[g.name] {
		return &unavailableDialer{d, fmt.Errorf("dialer-proxy cycle at group %q", g.name)}, []RelayHop{hop}, key + ":cycle"
	}
	seen[g.name] = true
	dep := s.groupByNameLocked(depName)
	if dep == nil || len(dep.dialers) == 0 {
		return &unavailableDialer{d, fmt.Errorf("dialer-proxy group %q is unavailable", depName)}, []RelayHop{{Group: depName}, hop}, key + ":unavailable:" + depName
	}
	di := s.activeDialerIndexLocked(dep)
	if choice, ok := choices[dep.name]; ok && choice >= 0 && choice < len(dep.dialers) {
		di = choice
	}
	upstream, path, upKey := s.resolvePathWithChoicesLocked(dep, dep.dialers[di], seen, bind, choices)
	key += "|" + upKey
	if bind {
		if temporary, ok := d.(interface{ BindDependencyForCheck(Dialer, string) Dialer }); ok && len(choices) > 0 {
			d = temporary.BindDependencyForCheck(upstream, key)
		} else {
			d = d.(interface{ BindDependency(Dialer, string) Dialer }).BindDependency(upstream, key)
		}
	}
	return d, append(path, hop), key
}

func (s *Selector) snapshotBenchmarkTargetLocked(target benchmarkTarget) benchmarkTarget {
	return s.snapshotBenchmarkTargetWithOverridesLocked(target, nil)
}

func (s *Selector) snapshotBenchmarkTargetWithOverridesLocked(target benchmarkTarget, choices map[string]int) benchmarkTarget {
	// Refresh targets captured before a configuration edit or earlier check level.
	g := s.groupByNameLocked(target.group.name)
	if g == nil {
		target.dialer = &unavailableDialer{target.dialer, fmt.Errorf("relay group %q removed", target.group.name)}
		return target
	}
	for i, d := range g.dialers {
		if d.Name() == target.dialer.Name() {
			target.group, target.index = g, i
			target.dialer, target.path, target.pathKey = s.resolvePathWithChoicesLocked(g, d, make(map[string]bool), true, choices)
			return target
		}
	}
	target.dialer = &unavailableDialer{target.dialer, fmt.Errorf("relay %q removed", target.dialer.Name())}
	return target
}

func (s *Selector) benchmarkTargetCurrentLocked(target benchmarkTarget) bool {
	g := s.groupByNameLocked(target.group.name)
	if g == nil {
		return false
	}
	for _, d := range g.dialers {
		if d.Name() == target.dialer.Name() {
			_, _, key := s.resolvePathLocked(g, d, make(map[string]bool), false)
			return target.pathKey == "" || key == target.pathKey
		}
	}
	return false
}

func (s *Selector) activePathLocked() ([]RelayHop, string) {
	if len(s.groups) == 0 {
		return nil, ""
	}
	g := s.groups[s.activeUsableGroupIndexLocked()]
	if len(g.dialers) == 0 {
		return nil, ""
	}
	_, path, key := s.resolvePathLocked(g, g.dialers[s.activeDialerIndexLocked(g)], make(map[string]bool), false)
	return path, key
}

func (s *Selector) selectedDependencyTargetsLocked() []benchmarkTarget {
	path, _ := s.activePathLocked()
	var targets []benchmarkTarget
	for i, hop := range path {
		if i == len(path)-1 || hop.Group == directGroupName {
			continue
		}
		g := s.groupByNameLocked(hop.Group)
		if g != nil && len(g.dialers) > 0 {
			di := s.activeDialerIndexLocked(g)
			targets = append(targets, benchmarkTarget{group: g, index: di, dialer: g.dialers[di]})
		}
	}
	return targets
}

func (s *Selector) invalidateChangedPathsLocked() {
	changed := false
	for _, g := range s.groups {
		for _, d := range g.dialers {
			h := s.health[s.healthKey(g.name, d.Name())]
			if h == nil {
				continue
			}
			_, path, key := s.resolvePathLocked(g, d, make(map[string]bool), false)
			if h.pathKey != "" && h.pathKey != key {
				h.Status = HealthPending
				h.Latency, h.URLTestLatency = 0, 0
				h.LastCheckedAt = time.Time{}
				h.Error = ""
				changed = true
			}
			h.pathKey, h.Path = key, path
		}
	}
	if changed {
		s.resetSelectedCheckFailuresLocked()
		s.notifyDependencyCheckLocked()
	}
}

func (s *Selector) notifyDependencyCheckLocked() {
	select {
	case s.dependencyCheckCh <- struct{}{}:
	default:
	}
}

func (s *Selector) dependencyCheckLoop() {
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.dependencyCheckCh:
			s.mu.RLock()
			var targets []benchmarkTarget
			for _, g := range s.groups {
				for i, d := range g.dialers {
					if h := s.health[s.healthKey(g.name, d.Name())]; h != nil && h.Status == HealthPending {
						targets = append(targets, benchmarkTarget{group: g, index: i, dialer: d})
					}
				}
			}
			s.mu.RUnlock()
			if len(targets) > 0 {
				_ = s.benchmarkTargets(targets)
			}
		}
	}
}

// dependencyLevels expands candidate dependencies and checks each group only once.
func (s *Selector) dependencyLevels(targets []benchmarkTarget) [][]benchmarkTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byGroup := make(map[string]map[string]benchmarkTarget)
	expanded := make(map[string]bool)
	var add func(benchmarkTarget)
	add = func(t benchmarkTarget) {
		if t.group.name == directGroupName {
			return
		}
		if byGroup[t.group.name] == nil {
			byGroup[t.group.name] = make(map[string]benchmarkTarget)
		}
		byGroup[t.group.name][t.dialer.Name()] = t
		dep := dependencyName(t.dialer)
		if dep == "" || expanded[dep] {
			return
		}
		expanded[dep] = true
		if g := s.groupByNameLocked(dep); g != nil {
			for i, d := range g.dialers {
				add(benchmarkTarget{group: g, index: i, dialer: d})
			}
		}
	}
	for _, t := range targets {
		add(t)
	}
	depths := make(map[string]int)
	visiting := make(map[string]bool)
	var depth func(string) int
	depth = func(name string) int {
		if n, ok := depths[name]; ok {
			return n
		}
		if visiting[name] {
			return 0 // Invalid refreshes are rejected before publication.
		}
		visiting[name] = true
		n := 0
		for _, t := range byGroup[name] {
			if dep := dependencyName(t.dialer); dep != "" && dep != directGroupName {
				n = max(n, depth(dep)+1)
			}
		}
		depths[name] = n
		return n
	}
	var levels [][]benchmarkTarget
	for _, g := range s.groups {
		if len(byGroup[g.name]) == 0 {
			continue
		}
		n := depth(g.name)
		for len(levels) <= n {
			levels = append(levels, nil)
		}
		for _, d := range g.dialers {
			if t, ok := byGroup[g.name][d.Name()]; ok {
				levels[n] = append(levels[n], t)
			}
		}
	}
	return levels
}
