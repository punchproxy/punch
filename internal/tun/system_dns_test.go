package tun

import (
	"reflect"
	"testing"
)

func TestSystemDNSOverrideMonitorsChangedDNSForRestore(t *testing.T) {
	current := []systemDNSState{{Name: "Wi-Fi", Servers: []string{"198.18.0.1"}}}
	var applied [][]systemDNSState
	var restored []systemDNSState

	override := newSystemDNSOverride(
		"198.18.0.1",
		[]systemDNSState{{Name: "Wi-Fi", Servers: []string{"223.5.5.5"}}},
		func() ([]systemDNSState, error) {
			return cloneSystemDNSStates(current), nil
		},
		func(states []systemDNSState, _ string) error {
			applied = append(applied, cloneSystemDNSStates(states))
			current = []systemDNSState{{Name: "Wi-Fi", Servers: []string{"198.18.0.1"}}}
			return nil
		},
		func(states []systemDNSState) error {
			restored = cloneSystemDNSStates(states)
			return nil
		},
	)

	current = []systemDNSState{{Name: "Wi-Fi", Servers: []string{"1.1.1.1", "8.8.8.8"}}}
	override.checkOnce()
	if len(applied) != 1 {
		t.Fatalf("apply count = %d, want 1", len(applied))
	}
	if got, want := applied[0][0].Servers, []string{"1.1.1.1", "8.8.8.8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("apply restore servers = %#v, want %#v", got, want)
	}

	if err := override.StopAndRestore(); err != nil {
		t.Fatalf("StopAndRestore() error = %v", err)
	}
	if got, want := restored[0].Servers, []string{"1.1.1.1", "8.8.8.8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored servers = %#v, want %#v", got, want)
	}
}

func TestRestoreStateFromObservedDropsPunchDNS(t *testing.T) {
	got := restoreStateFromObserved(systemDNSState{
		Name:    "Wi-Fi",
		Servers: []string{"198.18.0.1", "1.1.1.1"},
	}, "198.18.0.1")
	if want := []string{"1.1.1.1"}; !reflect.DeepEqual(got.Servers, want) {
		t.Fatalf("servers = %#v, want %#v", got.Servers, want)
	}
}
