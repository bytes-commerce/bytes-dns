// Package cloudflare implements the Cloudflare DNS API v4 as a DNS provider
// for bytes-dns.
//
// Authentication uses a Cloudflare API token (Bearer) that carries the
// "Zone:Read" and "Zone:DNS:Edit" permissions. The global API key is
// deliberately not supported.
package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bytes-commerce/bytes-dns/internal/dns"
	"github.com/bytes-commerce/bytes-dns/internal/httpx"
)

const (
	productionAPIBase = "https://api.cloudflare.com/client/v4"
	requestTimeout    = 15 * time.Second

	// defaultProxied is used when the configuration does not say otherwise.
	// Newly created records are proxied by Cloudflare unless the user
	// explicitly asks for a DNS-only record.
	defaultProxied = true

	// recordComment marks the records managed by bytes-dns.
	recordComment = "Auto-provisionized by Bytes-DNS."
)

// Options configures the Cloudflare client.
type Options struct {
	// APIToken is the Cloudflare API token used for authentication.
	APIToken string
	// Zone is the zone name the client operates on, e.g. "example.com".
	Zone string
	// ZoneID is the Cloudflare zone ID. When set, no zone lookup is needed.
	ZoneID string
	// BaseURL overrides the API base URL. Used by tests.
	BaseURL string
}

// Client talks to the Cloudflare DNS API.
type Client struct {
	zone    string
	zoneID  string
	apiBase string
	http    *httpx.Client
}

// New returns a client for the given options.
func New(opts Options) *Client {
	base := opts.BaseURL
	if base == "" {
		base = os.Getenv("CLOUDFLARE_API_BASE")
	}
	if base == "" {
		base = productionAPIBase
	}

	http := httpx.NewClient(requestTimeout)
	http.SetHeader("Authorization", "Bearer "+opts.APIToken)
	http.SetHeader("Content-Type", "application/json")
	http.SetHeader("Accept", "application/json")
	http.SetHeader("User-Agent", "bytes-dns/1.0 (+https://github.com/bytes-commerce/bytes-dns)")

	return &Client{
		zone:    normaliseName(opts.Zone),
		zoneID:  strings.TrimSpace(opts.ZoneID),
		apiBase: base,
		http:    http,
	}
}

// apiError is a single error entry of the Cloudflare response envelope.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// envelope is the common Cloudflare response wrapper.
type envelope struct {
	Success  bool            `json:"success"`
	Errors   []apiError      `json:"errors"`
	Messages []string        `json:"messages"`
	Result   json.RawMessage `json:"result"`
}

type zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type record struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

// FindZone returns the zone with the given name.
func (c *Client) FindZone(ctx context.Context, zoneName string) (*dns.Zone, error) {
	zoneName = normaliseName(zoneName)
	if zoneName == "" {
		return nil, errors.New("no zone name configured for Cloudflare")
	}

	// A cached zone ID saves a lookup, but only for the zone it belongs to.
	if c.zoneID != "" && zoneName == c.zone {
		return &dns.Zone{ID: dns.ZoneID(c.zoneID), Name: zoneName, Status: "active"}, nil
	}

	endpoint := fmt.Sprintf("%s/zones?name=%s", c.apiBase, url.QueryEscape(zoneName))
	var zones []zone
	if err := c.get(ctx, endpoint, &zones); err != nil {
		return nil, fmt.Errorf("looking up Cloudflare zone %q: %w", zoneName, err)
	}

	for i := range zones {
		z := &zones[i]
		if strings.EqualFold(normaliseName(z.Name), zoneName) {
			return &dns.Zone{ID: dns.ZoneID(z.ID), Name: normaliseName(z.Name), Status: z.Status}, nil
		}
	}

	return nil, fmt.Errorf("zone %q not found in Cloudflare - verify the zone name and that the API token has Zone:Read access", zoneName)
}

// FindZoneByRecord returns the zone that hosts the given record. Cloudflare
// requires the zone to be configured, so the record is checked against it.
func (c *Client) FindZoneByRecord(ctx context.Context, fqdn string) (*dns.Zone, error) {
	if c.zone == "" {
		return nil, errors.New("no Cloudflare zone configured - set cloudflare.zone in config.json")
	}

	record := normaliseName(fqdn)
	if !strings.HasSuffix(strings.ToLower(record), "."+strings.ToLower(c.zone)) {
		return nil, fmt.Errorf("record %q is not within the configured Cloudflare zone %q", record, c.zone)
	}

	return c.FindZone(ctx, c.zone)
}

// FindRRSet returns the record set for fqdn and recordType, or nil when the
// record does not exist.
func (c *Client) FindRRSet(ctx context.Context, zone *dns.Zone, fqdn, recordType string) (*dns.RRSet, error) {
	records, err := c.listRecords(ctx, zone, recordType, normaliseName(fqdn))
	if err != nil {
		return nil, err
	}

	for i := range records {
		r := &records[i]
		if !strings.EqualFold(r.Type, recordType) {
			continue
		}
		if !strings.EqualFold(normaliseName(r.Name), normaliseName(fqdn)) {
			continue
		}
		return toRRSet(zone, r), nil
	}

	return nil, nil
}

// UpdateRRSet replaces the content of an existing record. Only the content
// is patched, so the proxy status of the record stays untouched.
func (c *Client) UpdateRRSet(ctx context.Context, zone *dns.Zone, rrset *dns.RRSet, newValue string) (*dns.RRSet, error) {
	body := map[string]any{"content": newValue}
	endpoint := fmt.Sprintf("%s/zones/%s/dns_records/%s",
		c.apiBase, url.PathEscape(string(zone.ID)), url.PathEscape(rrset.ID))

	var updated record
	if err := c.send(ctx, http.MethodPatch, endpoint, body, &updated); err != nil {
		return nil, fmt.Errorf("updating %s record %q in zone %s: %w", rrset.Type, rrset.Name, zone.Name, err)
	}

	return toRRSet(zone, &updated), nil
}

// CreateRRSet creates a new record. The proxy status is only applied here:
// existing records keep whatever the user configured in Cloudflare.
func (c *Client) CreateRRSet(ctx context.Context, zone *dns.Zone, fqdn, recordType, value string, ttl int, opts *dns.CreateOptions) (*dns.RRSet, error) {
	proxied := defaultProxied
	if opts != nil && opts.Proxied != nil {
		proxied = *opts.Proxied
	}

	body := map[string]any{
		"type":    recordType,
		"name":    normaliseName(fqdn),
		"content": value,
		"ttl":     ttl,
		"proxied": proxied,
		"comment": recordComment,
	}
	endpoint := fmt.Sprintf("%s/zones/%s/dns_records", c.apiBase, url.PathEscape(string(zone.ID)))

	var created record
	if err := c.send(ctx, http.MethodPost, endpoint, body, &created); err != nil {
		return nil, fmt.Errorf("creating %s record %q in zone %s: %w", recordType, fqdn, zone.Name, err)
	}

	return toRRSet(zone, &created), nil
}

// listRecords returns the records of a zone that match the given type and
// name.
func (c *Client) listRecords(ctx context.Context, zone *dns.Zone, recordType, name string) ([]record, error) {
	params := url.Values{}
	params.Set("type", recordType)
	params.Set("name", name)
	params.Set("per_page", "100")

	endpoint := fmt.Sprintf("%s/zones/%s/dns_records?%s",
		c.apiBase, url.PathEscape(string(zone.ID)), params.Encode())

	var records []record
	err := c.get(ctx, endpoint, &records)
	if err != nil && httpx.StatusCode(err) == http.StatusBadRequest {
		// Older API versions reject the plain name filter. Fall back to
		// listing the zone and matching the name locally.
		return c.listRecordsUnfiltered(ctx, zone, recordType, name)
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s records in zone %s: %w", recordType, zone.Name, err)
	}

	return records, nil
}

// listRecordsUnfiltered lists all records of a type in a zone and filters
// them by name locally.
func (c *Client) listRecordsUnfiltered(ctx context.Context, zone *dns.Zone, recordType, name string) ([]record, error) {
	params := url.Values{}
	params.Set("type", recordType)
	params.Set("per_page", "1000")

	endpoint := fmt.Sprintf("%s/zones/%s/dns_records?%s",
		c.apiBase, url.PathEscape(string(zone.ID)), params.Encode())

	var all []record
	if err := c.get(ctx, endpoint, &all); err != nil {
		return nil, fmt.Errorf("listing %s records in zone %s: %w", recordType, zone.Name, err)
	}

	var matches []record
	for i := range all {
		if strings.EqualFold(normaliseName(all[i].Name), name) {
			matches = append(matches, all[i])
		}
	}

	return matches, nil
}

// get performs a GET request and decodes the "result" field of the response
// envelope into out.
func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	return c.send(ctx, http.MethodGet, endpoint, nil, out)
}

// send performs a request, decodes the response envelope and extracts its
// "result" field.
func (c *Client) send(ctx context.Context, method, endpoint string, body any, out any) error {
	var resp envelope

	switch method {
	case http.MethodGet:
		err := c.http.GetJSON(ctx, endpoint, &resp)
		if err != nil {
			return c.decorate(err)
		}
	case http.MethodPost, http.MethodPatch, http.MethodPut:
		err := c.http.SendJSON(ctx, method, endpoint, body, &resp)
		if err != nil {
			return c.decorate(err)
		}
	default:
		return fmt.Errorf("unsupported HTTP method %q", method)
	}

	if !resp.Success {
		return c.envelopeError(resp.Errors)
	}

	if out == nil || len(resp.Result) == 0 {
		return nil
	}

	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("decoding Cloudflare response: %w", err)
	}

	return nil
}

// envelopeError turns the error list of a response envelope into an error.
func (c *Client) envelopeError(errs []apiError) error {
	if len(errs) == 0 {
		return errors.New("the Cloudflare API request failed without an error message")
	}

	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Message != "" {
			messages = append(messages, e.Message)
			continue
		}
		messages = append(messages, fmt.Sprintf("code %d", e.Code))
	}

	return fmt.Errorf("the Cloudflare API request failed: %s", strings.Join(messages, "; "))
}

// decorate maps HTTP status codes to messages that point at the likely
// cause.
func (c *Client) decorate(err error) error {
	var httpErr *httpx.Error
	if !errors.As(err, &httpErr) {
		return err
	}

	switch httpErr.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("authentication failed (HTTP 401) - check the Cloudflare API token in config.json")
	case http.StatusForbidden:
		return fmt.Errorf("access denied (HTTP 403) - the Cloudflare API token may lack the Zone:DNS:Edit permission")
	case http.StatusNotFound:
		return fmt.Errorf("resource not found (HTTP 404) - check the Cloudflare zone id and record name")
	case http.StatusBadRequest:
		return fmt.Errorf("invalid request (HTTP 400): %s", httpErr.Body)
	default:
		return fmt.Errorf("unexpected HTTP %d from Cloudflare DNS API: %s", httpErr.StatusCode, httpErr.Body)
	}
}

// toRRSet maps a Cloudflare record onto the provider neutral record set.
func toRRSet(zone *dns.Zone, r *record) *dns.RRSet {
	proxied := r.Proxied
	return &dns.RRSet{
		ID:   r.ID,
		Name: normaliseName(r.Name),
		Type: strings.ToUpper(r.Type),
		TTL:  r.TTL,
		Records: []dns.RecordValue{
			{Value: r.Content, Comment: r.Comment},
		},
		Zone:    zone.ID,
		Proxied: &proxied,
	}
}

// normaliseName lower-cases a DNS name and strips the trailing dot.
func normaliseName(name string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(name)), ".")
}
