package client

import (
	"testing"
	"time"
)

func TestExitCircuitUsesRollingWindowAndMajorityThreshold(t *testing.T) {
	var breaker exitCircuit
	start := time.Unix(1000, 0)
	interval := 250 * time.Millisecond // timeout=1s => min(max(timeout/4,100ms),500ms)
	for i := 0; i < exitHealthWindow; i++ {
		now := start.Add(time.Duration(i) * interval)
		breaker.observe(now, now, time.Second)
	}
	for i := 0; i < exitHealthWindow/2; i++ {
		now := start.Add(time.Duration(exitHealthWindow+i) * interval)
		breaker.observe(now, time.Time{}, time.Second)
	}
	if breaker.state != "closed" || breaker.filled != exitHealthWindow || breaker.failures != exitHealthWindow/2 {
		t.Fatalf("exactly half failures in full rolling window opened circuit: %+v", breaker)
	}

	openAt := start.Add(time.Duration(exitHealthWindow+exitHealthWindow/2) * interval)
	breaker.observe(openAt, time.Time{}, time.Second)
	if breaker.state != "open" || breaker.filled != 0 || breaker.backoff != exitHealthBaseBackoff {
		t.Fatalf("majority failures did not open circuit with reset window: %+v", breaker)
	}
	if breaker.openUntil != openAt.Add(exitHealthBaseBackoff) {
		t.Fatalf("open interval = %s, want %s", breaker.openUntil.Sub(openAt), exitHealthBaseBackoff)
	}
}

func TestExitCircuitExponentialBackoffAndHalfOpenProbe(t *testing.T) {
	var breaker exitCircuit
	start := time.Unix(2000, 0)
	for i := 0; i < exitHealthMinSamples; i++ {
		breaker.observe(start.Add(time.Duration(i)*250*time.Millisecond), time.Time{}, time.Second)
	}
	if breaker.state != "open" {
		t.Fatalf("persistent failures did not open circuit: %+v", breaker)
	}

	firstProbe := breaker.openUntil
	staleAuth := start
	breaker.observe(firstProbe, staleAuth, time.Second)
	if breaker.state != "half-open" || !breaker.probeUntil.Equal(firstProbe.Add(time.Second)) {
		t.Fatalf("circuit did not enter bounded half-open probe: %+v", breaker)
	}
	breaker.observe(breaker.probeUntil, staleAuth, time.Second)
	if breaker.state != "open" || breaker.backoff != 2*exitHealthBaseBackoff {
		t.Fatalf("failed probe did not double reopen delay: %+v", breaker)
	}

	secondProbe := breaker.openUntil
	breaker.observe(secondProbe, staleAuth, time.Second)
	breaker.observe(secondProbe.Add(100*time.Millisecond), secondProbe.Add(time.Millisecond), time.Second)
	if breaker.state != "closed" || breaker.backoff != exitHealthBaseBackoff || breaker.filled != 0 {
		t.Fatalf("authenticated half-open response did not close/reset circuit: %+v", breaker)
	}
}

func TestAdaptiveTimeoutTracksEWMAAndBounds(t *testing.T) {
	configured := 100 * time.Millisecond
	if got := adaptiveTimeout(0, configured); got != configured {
		t.Fatalf("no RTT timeout = %s, want configured floor %s", got, configured)
	}
	if got, want := adaptiveTimeout(100, configured), 350*time.Millisecond; got != want {
		t.Fatalf("100ms RTT timeout = %s, want 3*RTT+50ms = %s", got, want)
	}
	if got, want := adaptiveTimeout(2_000, configured), maxAdaptiveTimeout; got != want {
		t.Fatalf("large RTT timeout = %s, want cap %s", got, want)
	}
	if got, want := adaptiveTimeout(100, 2*time.Second), 2*time.Second; got != want {
		t.Fatalf("configured floor was not preserved: got %s want %s", got, want)
	}
}
