package msgraph

import (
	"context"
	"math"
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

func uncappedRetryDelay(header http.Header, attempt int) time.Duration {
	if delay, ok := retryAfterDelay(header); ok {
		return delay
	}
	base := 100 * time.Millisecond
	pow := math.Pow(2, float64(attempt))
	jitter := time.Duration(rand.Int64N(int64(50 * time.Millisecond)))
	return time.Duration(pow)*base + jitter
}
