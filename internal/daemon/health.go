package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// HealthOpts configures health check polling behavior.
type HealthOpts struct {
	Interval    time.Duration // poll interval (default 500ms)
	MaxAttempts int           // max poll attempts (default 120 = 60s)
}

// HealthResult holds the outcome of a health check.
type HealthResult struct {
	PortListening bool
	HTTPHealthy   bool
	Elapsed       time.Duration
	Attempts      int
}

// WaitForHealthy polls the gateway until it responds or timeout.
// Checks TCP port connectivity and HTTP /health endpoint.
func WaitForHealthy(ctx context.Context, port int, bind string, opts HealthOpts) (*HealthResult, error) {
	if opts.Interval == 0 {
		opts.Interval = 500 * time.Millisecond
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 120 // 60s total
	}

	addr := net.JoinHostPort(bind, fmt.Sprintf("%d", port))
	healthURL := fmt.Sprintf("http://%s/health", addr)
	start := time.Now()

	client := &http.Client{Timeout: 2 * time.Second}

	for i := 0; i < opts.MaxAttempts; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Check TCP port
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			time.Sleep(opts.Interval)
			continue
		}
		conn.Close()

		// Check HTTP /health
		resp, err := client.Get(healthURL)
		if err != nil {
			time.Sleep(opts.Interval)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return &HealthResult{
				PortListening: true,
				HTTPHealthy:   true,
				Elapsed:       time.Since(start),
				Attempts:      i + 1,
			}, nil
		}

		time.Sleep(opts.Interval)
	}

	// Timeout — check final state
	result := &HealthResult{
		Elapsed:  time.Since(start),
		Attempts: opts.MaxAttempts,
	}

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		conn.Close()
		result.PortListening = true
	}

	return result, fmt.Errorf("health check timed out after %s (%d attempts)", result.Elapsed.Round(time.Second), result.Attempts)
}
