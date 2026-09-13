package relay

import "time"

const dependencyRecoveryCandidates = 8
const dependencyFailureRetry = time.Minute

// dependencyCandidateAllowedLocked avoids immediately reselecting an upstream
// which passed its own URL check but failed the active exit's complete path.
func (s *Selector) dependencyCandidateAllowedLocked(g *group, index int) bool {
	if len(s.dependencyFailures) == 0 || len(s.groups) == 0 {
		return true
	}
	path, _ := s.activePathLocked()
	used := false
	for _, hop := range path[:max(0, len(path)-1)] {
		used = used || hop.Group == g.name
	}
	if !used {
		return true
	}
	root := s.groups[s.activeUsableGroupIndexLocked()]
	if len(root.dialers) == 0 {
		return true
	}
	di := s.activeDialerIndexLocked(root)
	_, _, pathKey := s.resolvePathWithChoicesLocked(root, root.dialers[di], make(map[string]bool), false, map[string]int{g.name: index})
	return !time.Now().Before(s.dependencyFailures[pathKey])
}

func (s *Selector) rememberDependencyFailureLocked(pathKey string) {
	if s.dependencyFailures == nil {
		s.dependencyFailures = make(map[string]time.Time)
	}
	now := time.Now()
	for key, retry := range s.dependencyFailures {
		if !now.Before(retry) {
			delete(s.dependencyFailures, key)
		}
	}
	s.dependencyFailures[pathKey] = now.Add(dependencyFailureRetry)
}

// recoverSelectedDependencies tests bounded one-group changes to the failed
// exit path. It does not infer edge reachability from a standalone URL result.
// True means recovery succeeded or another benchmark/path change took over.
func (s *Selector) recoverSelectedDependencies(failed benchmarkTarget) bool {
	if len(failed.path) < 2 {
		return false
	}
	if !s.fullBenchmarkMu.TryLock() {
		return true
	}
	defer s.fullBenchmarkMu.Unlock()
	return s.recoverSelectedDependenciesDuringBenchmark(failed)
}

// The caller holds fullBenchmarkMu so a full check can repair the selected
// chain before considering a fallback to a different top-level group.
func (s *Selector) recoverSelectedDependenciesDuringBenchmark(failed benchmarkTarget) bool {
	if len(failed.path) < 2 {
		return false
	}

	s.mu.Lock()
	_, baseKey := s.activePathLocked()
	if baseKey != failed.pathKey || !s.benchmarkTargetCurrentLocked(failed) {
		s.mu.Unlock()
		return true
	}
	s.rememberDependencyFailureLocked(baseKey)
	type alternative struct {
		group string
		index int
	}
	var alternatives []alternative
	// Paths list the earliest dependency first. Try repairing that dependency
	// before replacing relays that themselves depend on it.
	for _, hop := range failed.path[:len(failed.path)-1] {
		g := s.groupByNameLocked(hop.Group)
		if g == nil || g.mode != "auto" {
			continue
		}
		for i := range g.dialers {
			if i != s.activeDialerIndexLocked(g) {
				alternatives = append(alternatives, alternative{g.name, i})
			}
		}
	}
	s.mu.Unlock()

	attempts := 0
	for _, alternative := range alternatives {
		if attempts >= dependencyRecoveryCandidates {
			break
		}
		choices := map[string]int{alternative.group: alternative.index}
		s.mu.RLock()
		_, currentKey := s.activePathLocked()
		if currentKey != baseKey {
			s.mu.RUnlock()
			return true
		}
		g := s.groupByNameLocked(failed.group.name)
		if g == nil {
			s.mu.RUnlock()
			return true
		}
		_, _, candidateKey := s.resolvePathWithChoicesLocked(g, g.dialers[s.activeDialerIndexLocked(g)], make(map[string]bool), false, choices)
		if time.Now().Before(s.dependencyFailures[candidateKey]) {
			s.mu.RUnlock()
			continue
		}
		candidate := s.snapshotBenchmarkTargetWithOverridesLocked(failed, choices)
		s.mu.RUnlock()
		attempts++

		sem := s.relayCheckSemaphore()
		sem <- struct{}{}
		check := s.testRelay(candidate.dialer)
		<-sem

		s.mu.Lock()
		_, currentKey = s.activePathLocked()
		g = s.groupByNameLocked(alternative.group)
		fresh := s.snapshotBenchmarkTargetWithOverridesLocked(failed, choices)
		if currentKey != baseKey || fresh.pathKey != candidate.pathKey || g == nil || g.mode != "auto" || alternative.index >= len(g.dialers) {
			s.mu.Unlock()
			return true
		}
		if check.err != nil {
			s.rememberDependencyFailureLocked(candidate.pathKey)
			s.mu.Unlock()
			continue
		}
		g.active.Store(int32(alternative.index))
		s.invalidateChangedPathsLocked()
		s.resetSelectedCheckFailuresLocked()
		s.saveSelectionsLocked()
		s.mu.Unlock()

		s.finishRelayCheck(candidate, check)
		s.applyOutsideConnectivityCheckResult(candidate, check)
		s.publishRelayChange(s.displayName(failed.group.name, failed.dialer.Name()))
		return true
	}
	return false
}
