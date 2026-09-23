package upstream

import (
	"context"
	"fmt"
	"time"

	"mcp-foyer/internal/config"
)

// RetryPolicy controls how a failed connection attempt is repeated.
type RetryPolicy struct {
	// MaxRetries is how many times to try again after the first attempt
	// fails. Zero means try once and give up.
	MaxRetries int

	// Backoff is the delay before the first retry. Each subsequent delay
	// doubles.
	Backoff time.Duration

	// MaxBackoff caps the delay. Without it, a handful of retries on a
	// server that is simply switched off would end up waiting hours.
	MaxBackoff time.Duration

	// Sleep waits for a duration, returning early if the context ends.
	// Tests replace it to observe the delays without living through
	// them.
	Sleep func(ctx context.Context, d time.Duration) error
}

// RetryPolicyFrom builds a policy from the startup configuration.
func RetryPolicyFrom(cfg config.Startup) RetryPolicy {
	return RetryPolicy{
		MaxRetries: cfg.MaxRetries,
		Backoff:    cfg.RetryBackoff,
		MaxBackoff: defaultMaxBackoff,
	}
}

// defaultMaxBackoff bounds the wait when a policy does not set one.
const defaultMaxBackoff = 2 * time.Minute

// delay returns how long to wait before the retry numbered n, counting
// from zero.
func (p RetryPolicy) delay(n int) time.Duration {
	if p.Backoff <= 0 {
		return 0
	}
	max := p.MaxBackoff
	if max <= 0 {
		max = defaultMaxBackoff
	}

	delay := p.Backoff
	for i := 0; i < n; i++ {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	if delay > max {
		return max
	}
	return delay
}

func (p RetryPolicy) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ConnectWithRetry connects, retrying with exponential backoff.
//
// A server that is slow to become available is the common case at
// startup — a machine that just booted, a container still starting — so
// the first failure is not treated as final.
func (c *Conn) ConnectWithRetry(ctx context.Context, policy RetryPolicy) error {
	var lastErr error

	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := policy.delay(attempt - 1)
			c.deps.logger().Info("retrying the connection",
				"attempt", attempt+1, "of", policy.MaxRetries+1, "after", delay)

			if err := policy.sleep(ctx, delay); err != nil {
				return fmt.Errorf("connect %s: gave up waiting to retry: %w", c.name, err)
			}
		}

		err := c.Connect(ctx)
		if err == nil {
			return nil
		}
		lastErr = err

		// A cancelled context will not be any better on the next
		// attempt, so stop rather than burn through the budget.
		if ctx.Err() != nil {
			return fmt.Errorf("connect %s: %w", c.name, ctx.Err())
		}
	}

	attempts := policy.MaxRetries + 1
	return fmt.Errorf("connect %s: gave up after %d attempt(s): %w", c.name, attempts, lastErr)
}
