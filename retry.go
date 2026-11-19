package msgraph

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

type sleepFunc func(context.Context, time.Duration) error

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func retryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout
}

// retryWait is a planned sleep before the next attempt, with enough of its
// derivation kept to explain it in a span event or log line.
type retryWait struct {
	delay time.Duration
	// retryAfter reports that the delay came from a Retry-After header.
	retryAfter bool
	// capped reports that WithMaxRetryDelay shortened the delay.
	capped bool
}

func planRetry(header http.Header, attempt int, maxDelay time.Duration) retryWait {
	delay, retryAfter := retryAfterDelay(header)
	if !retryAfter {
		delay = backoffDelay(attempt)
	}
	wait := retryWait{delay: delay, retryAfter: retryAfter}
	if maxDelay > 0 && delay > maxDelay {
		wait.delay, wait.capped = maxDelay, true
	}
	if wait.delay <= 0 {
		// Belt and braces: a zero or negative sleep would busy-loop the
		// retry path against the service.
		wait.delay = backoffBase
	}
	return wait
}

// retryAfterDelay parses a Retry-After header in either of its two RFC 9110
// spellings: delay-seconds, or an HTTP-date.
func retryAfterDelay(header http.Header) (time.Duration, bool) {
	value := header.Get("Retry-After")
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay, true
		}
	}
	return 0, false
}

const (
	backoffBase   = 100 * time.Millisecond
	backoffJitter = 50 * time.Millisecond
	// maxBackoffShift saturates the exponential term. backoffBase << 20 is
	// about 29 hours, far past any sane WithMaxRetryDelay, and shifting no
	// further keeps the arithmetic inside int64. The previous float
	// exponential overflowed at attempt 37: the delay wrapped negative, and
	// the maxDelay cap could not catch it because it only clamps values
	// above the cap, so retries ran with no backoff at all.
	maxBackoffShift = 20
)

func backoffDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > maxBackoffShift {
		attempt = maxBackoffShift
	}
	jitter := time.Duration(rand.Int64N(int64(backoffJitter)))
	return backoffBase<<uint(attempt) + jitter
}
