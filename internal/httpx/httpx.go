// Package httpx provides the HTTP plumbing shared by the DNS provider
// clients: request execution, a bounded retry policy and typed errors that
// carry the HTTP status code so each provider can map it to a helpful
// message.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// MaxRetries is the total number of attempts per request.
	MaxRetries = 3
	// RetryBackoffBase is the delay before the first retry; it doubles for
	// every further attempt.
	RetryBackoffBase = 250 * time.Millisecond
	// MaxBodyBytes caps how much of a response body is read.
	MaxBodyBytes = 1 << 20
	// maxErrBodyLen caps how much of an error body is included in messages.
	maxErrBodyLen = 256
)

// Error describes a response with a non-2xx status code.
type Error struct {
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// StatusCode returns the HTTP status code carried by err, or 0 if err is not
// an HTTP error.
func StatusCode(err error) int {
	var httpErr *Error
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

// Client executes HTTP requests with a bounded retry policy. Headers set via
// SetHeader are applied to every request.
type Client struct {
	HTTP   *http.Client
	header http.Header
}

// NewClient returns a client with the given per-request timeout.
func NewClient(timeout time.Duration) *Client {
	return &Client{
		HTTP:   &http.Client{Timeout: timeout},
		header: make(http.Header),
	}
}

// SetHeader sets a header that is sent with every request.
func (c *Client) SetHeader(key, value string) {
	c.header.Set(key, value)
}

// GetJSON performs a GET request and decodes the JSON response into out.
// out may be nil when the response body is not needed.
func (c *Client) GetJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	return c.Do(req, out)
}

// SendJSON marshals body, sends it as a JSON request and decodes the JSON
// response into out. out may be nil when the response body is not needed.
func (c *Client) SendJSON(ctx context.Context, method, endpoint string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshalling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}

	return c.Do(req, out)
}

// Do sends req, retrying on server errors, rate limits and network failures.
// It returns an *Error when the server answered with a non-2xx status.
func (c *Client) Do(req *http.Request, out any) error {
	for key, values := range c.header {
		for _, value := range values {
			req.Header.Set(key, value)
		}
	}

	// A request with a body can only be replayed when the caller supplied a
	// GetBody function. Without one we get a single attempt.
	if req.Body != nil && req.GetBody == nil {
		return c.do(req, out)
	}

	var lastErr error
	for attempt := 0; attempt < MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := RetryBackoffBase * time.Duration(1<<(attempt-1))
			select {
			case <-time.After(backoff):
			case <-req.Context().Done():
				return fmt.Errorf("retry aborted: %w", req.Context().Err())
			}
		}

		attemptReq := req
		if req.Body != nil {
			body, err := req.GetBody()
			if err != nil {
				return fmt.Errorf("retry body clone: %w", err)
			}
			clone := req.Clone(req.Context())
			clone.Body = body
			attemptReq = clone
		}

		lastErr = c.do(attemptReq, out)
		if lastErr == nil || !retryable(lastErr) {
			return lastErr
		}
	}

	return fmt.Errorf("after %d attempts: %w", MaxRetries, lastErr)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes))
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{StatusCode: resp.StatusCode, Body: Sanitise(rawBody)}
	}

	if out != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, out); err != nil {
			return fmt.Errorf("decoding API response: %w (body: %s)", err, Sanitise(rawBody))
		}
	}

	return nil
}

// retryable reports whether the request should be sent again.
func retryable(err error) bool {
	var httpErr *Error
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
	}
	// Connection level failures are worth another attempt.
	return strings.Contains(err.Error(), "HTTP request to")
}

// Sanitise trims and shortens a response body so it can be embedded in an
// error message.
func Sanitise(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > maxErrBodyLen {
		return s[:maxErrBodyLen] + "…"
	}
	return s
}
