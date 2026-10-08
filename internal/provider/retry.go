package provider

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

var (
	streamIdle  = 120 * time.Second
	retryBase   = 500 * time.Millisecond
	retryMax    = 10 * time.Second
	maxAttempts = 5
)

func waitRetry(ctx context.Context, err error, attempt int) error {
	delay := backoff(attempt)
	var api *APIError
	if errorsAs(err, &api) && api.RetryAfter > 0 {
		delay = api.RetryAfter
	}
	if delay > retryMax {
		delay = retryMax
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func backoff(attempt int) time.Duration {
	delay := retryBase
	for i := 1; i < attempt; i++ {
		if delay > retryMax/2 {
			delay = retryMax
			break
		}
		delay *= 2
	}
	jitter := 0.9 + rand.Float64()*0.2
	return time.Duration(float64(delay) * jitter)
}

func errorsAs(err error, target **APIError) bool {
	return errors.As(err, target)
}
