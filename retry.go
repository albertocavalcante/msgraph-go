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

func retryDelay(header http.Header, attempt int, maxDelay time.Duration) time.Duration {
	delay := uncappedRetryDelay(header, attempt)
	if maxDelay > 0 && delay > maxDelay {
		return maxDelay
	}
	if delay <= 0 {
		// Belt and braces: a zero or negative sleep would busy-loop the
		// retry path against the service.
		return backoffBase
	}
	return delay
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

func uncappedRetryDelay(header http.Header, attempt int) time.Duration {
	if delay, ok := retryAfterDelay(header); ok {
		return delay
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt > maxBackoffShift {
		attempt = maxBackoffShift
	}
	jitter := time.Duration(rand.Int64N(int64(backoffJitter)))
	return backoffBase<<uint(attempt) + jitter
}
