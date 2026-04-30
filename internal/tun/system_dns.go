package tun

import (
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

const dnsMonitorInterval = 10 * time.Second

type systemDNSState struct {
	Name    string
	Servers []string
	Empty   bool
	Content string
}

type SystemDNSInfo struct {
	Name           string   `json:"name"`
	Current        []string `json:"current"`
	OverriddenFrom []string `json:"overridden_from,omitempty"`
}

type systemDNSOverride struct {
	serverIP string
	read     func() ([]systemDNSState, error)
	apply    func([]systemDNSState, string) error
	restore  func([]systemDNSState) error

	mu     sync.RWMutex
	states []systemDNSState
	stop   chan struct{}
	done   chan struct{}
}

func newSystemDNSOverride(serverIP string, states []systemDNSState, read func() ([]systemDNSState, error), apply func([]systemDNSState, string) error, restore func([]systemDNSState) error) *systemDNSOverride {
	o := &systemDNSOverride{
		serverIP: serverIP,
		read:     read,
		apply:    apply,
		restore:  restore,
		states:   cloneSystemDNSStates(states),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go o.monitor()
	return o
}

func (o *systemDNSOverride) StopAndRestore() error {
	close(o.stop)
	<-o.done

	o.mu.RLock()
	states := cloneSystemDNSStates(o.states)
	o.mu.RUnlock()
	return o.restore(states)
}

func (o *systemDNSOverride) Snapshot() []SystemDNSInfo {
	current, err := o.read()
	if err != nil {
		slog.Warn("failed to inspect system DNS", "error", err)
		current = nil
	}

	o.mu.RLock()
	restoreByName := make(map[string][]string, len(o.states))
	for _, state := range o.states {
		restoreByName[state.Name] = cloneStrings(state.Servers)
	}
	o.mu.RUnlock()

	if len(current) == 0 {
		current = o.restoreStates()
	}

	info := make([]SystemDNSInfo, 0, len(current))
	for _, state := range current {
		entry := SystemDNSInfo{
			Name:    state.Name,
			Current: cloneStrings(state.Servers),
		}
		if restore, ok := restoreByName[state.Name]; ok {
			entry.OverriddenFrom = restore
		}
		info = append(info, entry)
	}
	return info
}

func (o *systemDNSOverride) restoreStates() []systemDNSState {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneSystemDNSStates(o.states)
}

func (o *systemDNSOverride) monitor() {
	defer close(o.done)

	ticker := time.NewTicker(dnsMonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-o.stop:
			return
		case <-ticker.C:
			o.checkOnce()
		}
	}
}

func (o *systemDNSOverride) checkOnce() {
	current, err := o.read()
	if err != nil {
		slog.Warn("failed to monitor system DNS", "error", err)
		return
	}

	var changed []systemDNSState
	for _, state := range current {
		if dnsStateIsOverridden(state, o.serverIP) {
			continue
		}
		changed = append(changed, state)
	}
	if len(changed) == 0 {
		return
	}

	o.mu.Lock()
	next := cloneSystemDNSStates(o.states)
	for _, state := range changed {
		next = upsertSystemDNSState(next, restoreStateFromObserved(state, o.serverIP))
	}
	o.states = next
	applyTargets := cloneSystemDNSStates(next)
	o.mu.Unlock()

	for _, state := range changed {
		slog.Warn("system DNS changed while Punch is running; preserving new DNS for restore and reapplying Punch DNS",
			"name", state.Name,
			"dns", formatDNSServersForLog(state),
			"punch_dns", o.serverIP,
		)
	}
	if err := o.apply(applyTargets, o.serverIP); err != nil {
		slog.Warn("failed to reapply system DNS override", "dns", o.serverIP, "error", err)
	}
}

func upsertSystemDNSState(states []systemDNSState, state systemDNSState) []systemDNSState {
	for i := range states {
		if states[i].Name == state.Name {
			states[i] = cloneSystemDNSState(state)
			return states
		}
	}
	return append(states, cloneSystemDNSState(state))
}

func dnsStateIsOverridden(state systemDNSState, serverIP string) bool {
	return !state.Empty && len(state.Servers) == 1 && state.Servers[0] == serverIP
}

func restoreStateFromObserved(state systemDNSState, serverIP string) systemDNSState {
	state = cloneSystemDNSState(state)
	if state.Empty {
		return state
	}
	servers := state.Servers[:0]
	for _, server := range state.Servers {
		if server != serverIP {
			servers = append(servers, server)
		}
	}
	state.Servers = servers
	state.Empty = len(state.Servers) == 0
	return state
}

func cloneSystemDNSStates(states []systemDNSState) []systemDNSState {
	out := make([]systemDNSState, len(states))
	for i, state := range states {
		out[i] = cloneSystemDNSState(state)
	}
	return out
}

func cloneSystemDNSState(state systemDNSState) systemDNSState {
	state.Servers = cloneStrings(state.Servers)
	return state
}

func cloneStrings(values []string) []string {
	return slices.Clone(values)
}

func formatDNSServersForLog(state systemDNSState) string {
	if state.Empty || len(state.Servers) == 0 {
		return "empty"
	}
	return strings.Join(state.Servers, ",")
}
