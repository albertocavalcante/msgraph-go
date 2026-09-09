package msgraph

import (
	"net/http"
	"testing"
	"time"
)

// A retry delay must never be zero or negative. The exponential term used to
// overflow int64: at attempt 60 it wrapped to exactly 0s and at 63 it went
// negative, and neither is caught by the maxDelay cap, which only clamps
// values above it. The effect was a retry loop with no backoff at all.
func TestRetryDelayNeverCollapses(t *testing.T) {
	for attempt := range 128 {
		uncapped := uncappedRetryDelay(http.Header{}, attempt)
		if uncapped <= 0 {
			t.Fatalf("uncappedRetryDelay(attempt=%d) = %v, want a positive delay", attempt, uncapped)
		}
		capped := retryDelay(http.Header{}, attempt, 30*time.Second)
		if capped <= 0 {
			t.Fatalf("retryDelay(attempt=%d) = %v, want a positive delay", attempt, capped)
		}
		if capped > 30*time.Second {
			t.Fatalf("retryDelay(attempt=%d) = %v, want it capped at 30s", attempt, capped)
		}
	}
}

// Backoff has to actually grow, or the cap is doing all the work.
func TestRetryDelayGrowsThenSaturates(t *testing.T) {
	previous := time.Duration(0)
	for attempt := range 8 {
		// Compare without jitter by taking the floor of the range.
		delay := uncappedRetryDelay(http.Header{}, attempt)
		if delay <= previous {
			t.Fatalf("attempt %d delay %v did not exceed previous %v", attempt, delay, previous)
		}
		previous = delay
	}
	// Beyond the shift ceiling the delay stops growing rather than wrapping.
	high := uncappedRetryDelay(http.Header{}, 60)
	higher := uncappedRetryDelay(http.Header{}, 120)
	if high <= 0 || higher <= 0 {
		t.Fatalf("saturated delays must stay positive: %v, %v", high, higher)
	}
	if diff := higher - high; diff > time.Minute || diff < -time.Minute {
		t.Fatalf("delay kept changing after saturation: %v vs %v", high, higher)
	}
}

func TestRetryDelayNegativeAttemptTreatedAsFirst(t *testing.T) {
	if got := uncappedRetryDelay(http.Header{}, -5); got <= 0 {
		t.Fatalf("uncappedRetryDelay(-5) = %v, want a positive delay", got)
	}
}

// Retry-After wins over the computed backoff, in both spellings.
func TestRetryAfterHeaderWins(t *testing.T) {
	seconds := http.Header{"Retry-After": []string{"12"}}
	if got := uncappedRetryDelay(seconds, 3); got != 12*time.Second {
		t.Fatalf("delay = %v, want 12s", got)
	}

	date := http.Header{"Retry-After": []string{time.Now().Add(20 * time.Second).UTC().Format(http.TimeFormat)}}
	got := uncappedRetryDelay(date, 3)
	if got < 15*time.Second || got > 21*time.Second {
		t.Fatalf("delay = %v, want roughly 20s", got)
	}
}

// A Retry-After in the past, or one that is unparseable, must fall back to the
// computed backoff rather than producing a zero or negative wait.
func TestRetryAfterIgnoredWhenStaleOrInvalid(t *testing.T) {
	for _, value := range []string{
		time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat),
		"not-a-delay",
		"-30",
		"",
	} {
		header := http.Header{"Retry-After": []string{value}}
		if got := uncappedRetryDelay(header, 1); got <= 0 {
			t.Fatalf("Retry-After %q produced delay %v, want a positive fallback", value, got)
		}
	}
}
