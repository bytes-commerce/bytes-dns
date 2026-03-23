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
		"name":     {zoneName},
		"mode":     {"primary"},
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

	return nil, fmt.Errorf("zone %q not found — verify the zone exists in your Hetzner account and the API token has access", zoneName)
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
	body := updateRRSetRequest{
		Records: []RecordValue{
			{
				Value:   newValue,
				Comment: "Auto-provisionized by Bytes-DNS.",
			},
		},
	}

	endpoint := fmt.Sprintf("%s/zones/%s/rrsets/%s/%s", c.apiBase, url.PathEscape(zoneID), url.PathEscape(rrset.Name), url.PathEscape(rrset.Type))

	if err := c.put(ctx, endpoint, body, nil); err != nil {
		return nil, fmt.Errorf("updating rrset %s (%s) in zone %s: %w", rrset.Name, rrset.Type, zoneID, err)
	}

	rrset.Records = body.Records
	return rrset, nil
}

func (c *Client) CreateRRSet(ctx context.Context, zoneID, name, recordType, value string, ttl int) (*RRSet, error) {
	body := createRRSetRequest{
		Name: name,
		Type: recordType,
		TTL:  ttl,
		Records: []RecordValue{
			{
				Value:   value,
				Comment: "Auto-provisionized by Bytes-DNS.",
			},
		},
		Labels: map[string]string{
			"bytes-dns": "success",
		},
	}

	endpoint := fmt.Sprintf("%s/zones/%s/rrsets", c.apiBase, url.PathEscape(zoneID))

	var result rrsetResponse
	if err := c.post(ctx, endpoint, body, &result); err != nil {
		return nil, fmt.Errorf("creating %s record %q in zone %s: %w", recordType, name, zoneID, err)
	}

	return &result.RRSet, nil
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
	req.Header.Set("User-Agent", "bytes-dns/1.0 (+https://github.com/bytes-commerce/bytes-dns)")
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
