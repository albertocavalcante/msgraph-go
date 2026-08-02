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

func uncappedRetryDelay(header http.Header, attempt int) time.Duration {
	if value := header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if when, err := http.ParseTime(value); err == nil {
			delay := time.Until(when)
			if delay > 0 {
				return delay
			}
		}
	}
	base := 100 * time.Millisecond
	pow := math.Pow(2, float64(attempt))
	jitter := time.Duration(rand.Int64N(int64(50 * time.Millisecond)))
	return time.Duration(pow)*base + jitter
}
