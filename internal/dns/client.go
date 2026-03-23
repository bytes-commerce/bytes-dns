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
	productionAPIBase = "https://dns.hetzner.com/api/v1"
	requestTimeout    = 15 * time.Second
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
	params := url.Values{"name": {zoneName}}
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

	return nil, fmt.Errorf("zone %q not found — verify the zone exists in your Hetzner account and the API token has access", zoneName)
}

func (c *Client) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	endpoint := fmt.Sprintf("%s/records?zone_id=%s", c.apiBase, url.QueryEscape(zoneID))

	var result recordsResponse
	if err := c.get(ctx, endpoint, &result); err != nil {
		return nil, fmt.Errorf("listing records for zone %s: %w", zoneID, err)
	}

	return result.Records, nil
}

func (c *Client) FindRecord(ctx context.Context, zoneID, name, recordType string) (*Record, error) {
	records, err := c.ListRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}

	for i := range records {
		r := &records[i]
		if strings.EqualFold(r.Type, recordType) && strings.EqualFold(r.Name, name) {
			return r, nil
		}
	}

	return nil, nil
}

func (c *Client) UpdateRecord(ctx context.Context, record *Record, newValue string, ttl int) (*Record, error) {
	body := updateRecordRequest{
		ZoneID: record.ZoneID,
		Type:   record.Type,
		Name:   record.Name,
		Value:  newValue,
		TTL:    ttl,
	}

	endpoint := fmt.Sprintf("%s/records/%s", c.apiBase, url.PathEscape(record.ID))

	var result recordResponse
	if err := c.put(ctx, endpoint, body, &result); err != nil {
		return nil, fmt.Errorf("updating record %s (%s %s): %w", record.ID, record.Type, record.Name, err)
	}

	return &result.Record, nil
}

func (c *Client) CreateRecord(ctx context.Context, zoneID, name, recordType, value string, ttl int) (*Record, error) {
	body := createRecordRequest{
		ZoneID: zoneID,
		Type:   recordType,
		Name:   name,
		Value:  value,
		TTL:    ttl,
	}

	endpoint := fmt.Sprintf("%s/records", c.apiBase)

	var result recordResponse
	if err := c.post(ctx, endpoint, body, &result); err != nil {
		return nil, fmt.Errorf("creating %s record %q in zone %s: %w", recordType, name, zoneID, err)
	}

	return &result.Record, nil
}

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	c.setHeaders(req)

	return c.do(req, out)
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

	return c.do(req, out)
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bytes-dns/1.0 (+https://github.com/bytesbytes/bytes-dns)")
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
