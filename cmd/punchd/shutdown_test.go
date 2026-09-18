package main

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestShutdownSecondInterruptSkipsAllWaits(t *testing.T) {
	for _, blockedPhase := range []string{"network restoration", "services and database"} {
		t.Run(blockedPhase, func(t *testing.T) {
			signals := make(chan os.Signal, 1)
			blocked := make(chan struct{})
			release := make(chan struct{})
			cleanupDone := make(chan struct{})
			defer func() {
				close(release)
				select {
				case <-cleanupDone:
				case <-time.After(time.Second):
					t.Error("test cleanup did not finish")
				}
			}()
			wait := func() {
				close(blocked)
				<-release
			}
			result := make(chan bool, 1)
			go func() {
				result <- runShutdown(signals, func() error {
					if blockedPhase == "network restoration" {
						wait()
					}
					return nil
				}, func() {
					defer close(cleanupDone)
					if blockedPhase == "services and database" {
						wait()
					}
				})
			}()
			select {
			case <-blocked:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not enter the blocked phase")
			}
			signals <- os.Interrupt
			select {
			case forced := <-result:
				if !forced {
					t.Fatal("second interrupt did not force exit")
				}
			case <-time.After(time.Second):
				t.Fatal("second interrupt waited for cleanup")
			}
		})
	}
}

func TestShutdownRestoresNetworkBeforeServices(t *testing.T) {
	for _, restoreErr := range []error{nil, errors.New("restore failed")} {
		restored := false
		stopped := false
		forced := runShutdown(make(chan os.Signal), func() error {
			restored = true
			return restoreErr
		}, func() {
			if !restored {
				t.Error("services stopped before network restoration")
			}
			stopped = true
		})
		if forced || !stopped {
			t.Fatalf("forced = %v, services stopped = %v", forced, stopped)
		}
	}
}
