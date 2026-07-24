package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	productionAPIBase = "https://api.hetzner.cloud/v1"
	requestTimeout    = 15 * time.Second

	maxRetries       = 3
	retryBackoffBase = 250 * time.Millisecond

	// actionPollInterval is how often we poll the Hetzner action
	// status endpoint after firing an rrset action.
	actionPollInterval = 500 * time.Millisecond
	// actionWaitTimeout is the maximum time we wait for an action
	// to reach a terminal state before giving up.
	actionWaitTimeout = 30 * time.Second
)

type Client struct {
	token      string
	apiBase    string
	httpClient *http.Client
}

func New(token string) *Client {
	return NewWithBaseURL(token, productionAPIBase)
}

func NewWithBaseURL(token, baseURL string) *Client {
	return &Client{
		token:   token,
		apiBase: baseURL,
		httpClient: &http.Client{
			Timeout: requestTimeout,
		},
	}
}

func (c *Client) FindZone(ctx context.Context, zoneName string) (*Zone, error) {
	params := url.Values{
		"per_page": {"999"},
	}
	endpoint := fmt.Sprintf("%s/zones?%s", c.apiBase, params.Encode())

	var result zonesResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("listing zones: %w", err)
	}

	for _, z := range result.Zones {
		if strings.EqualFold(z.Name, zoneName) {
			return &z, nil
		}
	}

	return nil, fmt.Errorf("zone %q not found - verify the zone exists in your Hetzner account and the API token has access", zoneName)
}

func (c *Client) FindZoneByRecord(ctx context.Context, recordName string) (*Zone, error) {
	params := url.Values{
		"per_page": {"999"},
	}
	endpoint := fmt.Sprintf("%s/zones?%s", c.apiBase, params.Encode())

	var result zonesResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("listing zones: %w", err)
	}

	record := strings.ToLower(strings.TrimRight(recordName, "."))
	var bestMatch *Zone
	for i := range result.Zones {
		z := &result.Zones[i]
		zoneName := strings.ToLower(strings.TrimRight(z.Name, "."))
		if record == zoneName || strings.HasSuffix(record, "."+zoneName) {
			if bestMatch == nil || len(zoneName) > len(bestMatch.Name) {
				bestMatch = z
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, nil
	}

	return nil, fmt.Errorf("no matching zone found for record %q in your Hetzner account", recordName)
}

func (c *Client) CreateZone(ctx context.Context, name string, ttl int) (*Zone, error) {
	req := CreateZoneRequest{
		Name: name,
		TTL:  ttl,
	}
	var resp createZoneResponse
	endpoint := fmt.Sprintf("%s/zones", c.apiBase)
	if err := c.post(ctx, endpoint, req, &resp); err != nil {
		return nil, err
	}
	return &resp.Zone, nil
}

func (c *Client) ListRRSets(ctx context.Context, zoneID string, name string, recordType string) ([]RRSet, error) {
	params := url.Values{
		"per_page": {"100"},
	}
	if name != "" {
		params.Set("name", name)
	}
	if recordType != "" {
		params.Set("type", recordType)
	}

	endpoint := fmt.Sprintf("%s/zones/%s/rrsets?%s", c.apiBase, url.PathEscape(zoneID), params.Encode())

	var result rrsetsResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("listing rrsets for zone %s: %w", zoneID, err)
	}

	return result.RRSets, nil
}

func (c *Client) FindRRSet(ctx context.Context, zoneID, name, recordType string) (*RRSet, error) {
	rrsets, err := c.ListRRSets(ctx, zoneID, name, recordType)
	if err != nil {
		return nil, err
	}

	for i := range rrsets {
		r := &rrsets[i]
		if strings.EqualFold(r.Type, recordType) && strings.EqualFold(r.Name, name) {
			return r, nil
		}
	}

	return nil, nil
}

func (c *Client) UpdateRRSet(ctx context.Context, zoneID string, rrset *RRSet, newValue string) (*RRSet, error) {
	// Hetzner migrated the rrset update from PUT to an action endpoint
	// (POST /zones/{id}/rrsets/{name}/{type}/actions/set_records) in
	// late 2025. The old PUT endpoint returns 422 with
	// "can't update records with this endpoint".
	body := setRecordsRequest{
		Records: []RecordValue{
			{
				Value:   newValue,
				Comment: "Auto-provisionized by Bytes-DNS.",
			},
		},
	}

	endpoint := fmt.Sprintf("%s/zones/%s/rrsets/%s/%s/actions/set_records",
		c.apiBase, url.PathEscape(zoneID), url.PathEscape(rrset.Name), url.PathEscape(rrset.Type))

	var resp actionResponse
	if err := c.post(ctx, endpoint, body, &resp); err != nil {
		return nil, fmt.Errorf("updating rrset %s (%s) in zone %s: %w", rrset.Name, rrset.Type, zoneID, err)
	}

	if err := c.waitForAction(ctx, resp.Action.ID); err != nil {
		return nil, fmt.Errorf("waiting for set_records action on %s (%s) in zone %s: %w", rrset.Name, rrset.Type, zoneID, err)
	}

	// Return a copy so we don't mutate the caller's RRSet.
	updated := *rrset
	updated.Records = make([]RecordValue, len(body.Records))
	copy(updated.Records, body.Records)
	return &updated, nil
}

func (c *Client) CreateRRSet(ctx context.Context, zoneID, name, recordType, value string, ttl int) (*RRSet, error) {
	// Hetzner's rrset create flow also uses the action endpoint:
	// POST /zones/{id}/rrsets/{name}/{type}/actions/set_records. The
	// set_records action creates the rrset if it doesn't exist.
	body := setRecordsRequest{
		Records: []RecordValue{
			{
				Value:   value,
				Comment: "Auto-provisionized by Bytes-DNS.",
			},
		},
		TTL: &ttl,
	}

	endpoint := fmt.Sprintf("%s/zones/%s/rrsets/%s/%s/actions/set_records",
		c.apiBase, url.PathEscape(zoneID), url.PathEscape(name), url.PathEscape(recordType))

	var resp actionResponse
	if err := c.post(ctx, endpoint, body, &resp); err != nil {
		return nil, fmt.Errorf("creating %s record %q in zone %s: %w", recordType, name, zoneID, err)
	}

	if err := c.waitForAction(ctx, resp.Action.ID); err != nil {
		return nil, fmt.Errorf("waiting for set_records action on %s (%s) in zone %s: %w", name, recordType, zoneID, err)
	}

	return &RRSet{
		Name:    name,
		Type:    recordType,
		TTL:     ttl,
		Zone:    parseZoneID(zoneID),
		Records: body.Records,
	}, nil
}

// parseZoneID converts a zoneID string to an int. Returns 0 on parse failure.
func parseZoneID(s string) int {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// waitForAction polls the Hetzner /actions/{id} endpoint until the
// action reaches status "success" or "error", or the timeout expires.
func (c *Client) waitForAction(ctx context.Context, id int) error {
	deadline := time.Now().Add(actionWaitTimeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for action %d after %s", id, actionWaitTimeout)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled while waiting for action %d: %w", id, err)
		}

		endpoint := fmt.Sprintf("%s/actions/%d", c.apiBase, id)
		var resp actionResponse
		if err := c.get(ctx, endpoint, &resp); err != nil {
			return fmt.Errorf("polling action %d: %w", id, err)
		}

		switch resp.Action.Status {
		case "success":
			return nil
		case "error":
			msg := "unknown error"
			if resp.Action.Error != nil {
				msg = resp.Action.Error.Message
			}
			return fmt.Errorf("action %d failed: %s", id, msg)
		case "running":
			// fall through to sleep + retry
		default:
			// Unknown status — treat as running.
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(actionPollInterval):
		}
	}
}

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	c.setHeaders(req)
	return c.doWithRetry(req, out)
}

func (c *Client) put(ctx context.Context, endpoint string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPut, endpoint, body, out)
}

func (c *Client) post(ctx context.Context, endpoint string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPost, endpoint, body, out)
}

func (c *Client) sendJSON(ctx context.Context, method, endpoint string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshalling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}

	return c.doWithRetry(req, out)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bytes-dns/1.0 (+https://github.com/bytes-commerce/bytes-dns)")
}

// doWithRetry executes the HTTP request with a retry policy.
// Retries on HTTP 5xx, 429, and network errors. Does not retry on 4xx.
func (c *Client) doWithRetry(req *http.Request, out any) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := retryBackoffBase * (1 << (attempt - 1)) // 250ms, 500ms, 1s
			select {
			case <-time.After(backoff):
			case <-req.Context().Done():
				return fmt.Errorf("retry aborted: %w", req.Context().Err())
			}
		}

		// Re-create the request body for retries if needed.
		var bodyReader io.ReadCloser
		if req.Body != nil {
			if req.GetBody == nil {
				// Caller didn't supply GetBody; can't safely retry.
				return c.do(req, out)
			}
			rc, err := req.GetBody()
			if err != nil {
				return fmt.Errorf("retry body clone: %w", err)
			}
			bodyReader = rc
		}

		attemptReq := req
		if bodyReader != nil {
			clone := req.Clone(req.Context())
			clone.Body = bodyReader
			attemptReq = clone
		}

		lastErr = c.do(attemptReq, out)
		if lastErr == nil || !shouldRetry(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("after %d attempts: %w", maxRetries, lastErr)
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "HTTP 429"):
		return true
	case strings.Contains(msg, "HTTP 5"):
		return true
	case strings.Contains(msg, "HTTP request to"):
		return true // network / connection error
	default:
		return false
	}
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to %s failed: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("authentication failed (HTTP 401) — check your api_token in config.json")
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("access denied (HTTP 403) — the API token may lack required permissions")
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("resource not found (HTTP 404) at %s", req.URL.Path)
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return fmt.Errorf("invalid request (HTTP 422): %s", sanitisedBody(rawBody))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP %d from Hetzner DNS API: %s", resp.StatusCode, sanitisedBody(rawBody))
	}

	if out != nil && len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, out); err != nil {
			return fmt.Errorf("decoding API response: %w (body: %s)", err, sanitisedBody(rawBody))
		}
	}

	return nil
}

func sanitisedBody(b []byte) string {
	const maxLen = 256
	s := strings.TrimSpace(string(b))
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}
