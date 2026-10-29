package dns

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bytes-commerce/bytes-dns/internal/httpx"
)

const (
	productionAPIBase = "https://api.hetzner.cloud/v1"
	requestTimeout    = 15 * time.Second

	// actionPollInterval is how often we poll the Hetzner action
	// status endpoint after firing an rrset action.
	actionPollInterval = 500 * time.Millisecond
	// actionWaitTimeout is the maximum time we wait for an action
	// to reach a terminal state before giving up.
	actionWaitTimeout = 30 * time.Second
)

type Client struct {
	apiBase string
	http    *httpx.Client
}

func New(token string) *Client {
	base := os.Getenv("HETZNER_API_BASE")
	if base == "" {
		base = productionAPIBase
	}
	return NewWithBaseURL(token, base)
}

func NewWithBaseURL(token, baseURL string) *Client {
	http := httpx.NewClient(requestTimeout)
	http.SetHeader("Authorization", "Bearer "+token)
	http.SetHeader("Accept", "application/json")
	http.SetHeader("User-Agent", "bytes-dns/1.0 (+https://github.com/bytes-commerce/bytes-dns)")

	return &Client{
		apiBase: baseURL,
		http:    http,
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

	for i := range result.Zones {
		z := &result.Zones[i]
		if strings.EqualFold(z.Name, zoneName) {
			return z, nil
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

// FindRRSet looks up a record set. The record is addressed by its fully
// qualified name; Hetzner stores it relative to the zone, so the name is
// converted into a label first.
func (c *Client) FindRRSet(ctx context.Context, zone *Zone, fqdn, recordType string) (*RRSet, error) {
	label, err := RecordLabel(fqdn, zone.Name)
	if err != nil {
		return nil, err
	}

	rrsets, err := c.ListRRSets(ctx, string(zone.ID), label, recordType)
	if err != nil {
		return nil, err
	}

	for i := range rrsets {
		r := &rrsets[i]
		if strings.EqualFold(r.Type, recordType) && strings.EqualFold(r.Name, label) {
			return r, nil
		}
	}

	return nil, nil
}

func (c *Client) UpdateRRSet(ctx context.Context, zone *Zone, rrset *RRSet, newValue string) (*RRSet, error) {
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
		c.apiBase, url.PathEscape(string(zone.ID)), url.PathEscape(rrset.Name), url.PathEscape(rrset.Type))

	var resp actionResponse
	if err := c.post(ctx, endpoint, body, &resp); err != nil {
		return nil, fmt.Errorf("updating rrset %s (%s) in zone %s: %w", rrset.Name, rrset.Type, zone.ID, err)
	}

	if err := c.waitForAction(ctx, resp.Action.ID); err != nil {
		return nil, fmt.Errorf("waiting for set_records action on %s (%s) in zone %s: %w", rrset.Name, rrset.Type, zone.ID, err)
	}

	// Return a copy so we don't mutate the caller's RRSet.
	updated := *rrset
	updated.Zone = zone.ID
	updated.Records = make([]RecordValue, len(body.Records))
	copy(updated.Records, body.Records)
	return &updated, nil
}

func (c *Client) CreateRRSet(ctx context.Context, zone *Zone, fqdn, recordType, value string, ttl int, _ *CreateOptions) (*RRSet, error) {
	label, err := RecordLabel(fqdn, zone.Name)
	if err != nil {
		return nil, err
	}

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
		c.apiBase, url.PathEscape(string(zone.ID)), url.PathEscape(label), url.PathEscape(recordType))

	var resp actionResponse
	if err := c.post(ctx, endpoint, body, &resp); err != nil {
		return nil, fmt.Errorf("creating %s record %q in zone %s: %w", recordType, fqdn, zone.ID, err)
	}

	if err := c.waitForAction(ctx, resp.Action.ID); err != nil {
		return nil, fmt.Errorf("waiting for set_records action on %s (%s) in zone %s: %w", label, recordType, zone.ID, err)
	}

	return &RRSet{
		Name:    label,
		Type:    recordType,
		TTL:     ttl,
		Zone:    zone.ID,
		Records: body.Records,
	}, nil
}

// RecordLabel converts a fully qualified record name into the label Hetzner
// stores it under, e.g. "home.example.com" becomes "home" for the zone
// "example.com".
func RecordLabel(fqdn, zoneName string) (string, error) {
	fqdn = strings.TrimRight(strings.TrimSpace(fqdn), ".")
	zoneName = strings.TrimRight(strings.TrimSpace(zoneName), ".")

	if fqdn == "" || zoneName == "" {
		return "", fmt.Errorf("record name and zone name are required")
	}
	if strings.EqualFold(fqdn, zoneName) {
		return "", fmt.Errorf("refusing to operate on the zone apex record %q - use a subdomain instead (e.g. \"home.%s\")", fqdn, zoneName)
	}

	suffix := "." + zoneName
	if !strings.HasSuffix(strings.ToLower(fqdn), strings.ToLower(suffix)) {
		return "", fmt.Errorf("record %q is not within zone %q", fqdn, zoneName)
	}

	return fqdn[:len(fqdn)-len(suffix)], nil
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
			// Unknown status - treat as running.
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(actionPollInterval):
		}
	}
}

func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	if err := c.http.GetJSON(ctx, endpoint, out); err != nil {
		return c.decorate(err, endpoint)
	}
	return nil
}

func (c *Client) post(ctx context.Context, endpoint string, body any, out any) error {
	if err := c.http.SendJSON(ctx, http.MethodPost, endpoint, body, out); err != nil {
		return c.decorate(err, endpoint)
	}
	return nil
}

// decorate turns the generic HTTP errors of the shared client into
// messages that point at the likely cause.
func (c *Client) decorate(err error, endpoint string) error {
	var httpErr *httpx.Error
	if !errors.As(err, &httpErr) {
		return err
	}

	switch httpErr.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("authentication failed (HTTP 401) - check your api_token in config.json")
	case http.StatusForbidden:
		return fmt.Errorf("access denied (HTTP 403) - the API token may lack required permissions")
	case http.StatusNotFound:
		return fmt.Errorf("resource not found (HTTP 404) at %s", endpoint)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("invalid request (HTTP 422): %s", httpErr.Body)
	default:
		return fmt.Errorf("unexpected HTTP %d from Hetzner DNS API: %s", httpErr.StatusCode, httpErr.Body)
	}
}
