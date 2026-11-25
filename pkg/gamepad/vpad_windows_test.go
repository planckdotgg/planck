//go:build windows

package gamepad

import (
	"testing"
	"time"
)

func TestRetryExpiredReportIsNotDelivered(t *testing.T) {
	tg := &vigemTarget{plugged: true, pending: State{}, hasPending: true, retrying: true, retryUntil: time.Now().Add(-time.Millisecond)}
	tg.retry()
	if tg.hasPending || tg.retrying {
		t.Fatalf("retry kept going: pending %v retrying %v", tg.hasPending, tg.retrying)
	}
	if tg.waitDelivered(10 * time.Millisecond) {
		t.Error("waitDelivered reported an expired, never-sent release as delivered")
	}
}
