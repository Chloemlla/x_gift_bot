package checkout

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const maxAttempts = 3

type temporaryError struct {
	cause error
	delay time.Duration
}

func (e *temporaryError) Error() string { return e.cause.Error() }
func (e *temporaryError) Unwrap() error { return e.cause }
func temporary(err error) error         { return &temporaryError{cause: err} }
func httpFailure(err error, status int, retryAfter string) error {
	if status != 429 && (status < 500 || status > 599) {
		return err
	}
	delay := time.Duration(0)
	if seconds, e := strconv.Atoi(retryAfter); e == nil && seconds > 0 {
		if seconds > 60 {
			return err
		}
		delay = time.Duration(seconds) * time.Second
	} else if date, e := http.ParseTime(retryAfter); e == nil {
		delay = time.Until(date)
		if delay > 60*time.Second {
			return err
		}
	}
	return &temporaryError{cause: err, delay: delay}
}
func waitRetry(ctx context.Context, err error, attempt int) error {
	var retry *temporaryError
	if attempt >= maxAttempts || !errors.As(err, &retry) {
		return err
	}
	delay := time.Duration(attempt*2) * time.Second
	if retry.delay > delay {
		delay = retry.delay
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return err
	}
	progress(ctx, -1, fmt.Sprintf("连接暂时不稳定，正在自动恢复（第 %d/%d 次）…", attempt+1, maxAttempts))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Only called at explicitly safe operations. Never wrap payment confirmation.
func retrySafe(ctx context.Context, call func() error) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		if err == nil {
			return nil
		}
		if stop := waitRetry(ctx, err, attempt); stop != nil {
			return stop
		}
	}
}
