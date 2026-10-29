package dns

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ZoneID identifies a DNS zone. Hetzner uses numeric IDs, Cloudflare uses
// 32 character hex strings, so the ID is kept as an opaque string.
type ZoneID string

func (z ZoneID) String() string { return string(z) }

// UnmarshalJSON accepts both JSON numbers (Hetzner) and JSON strings
// (Cloudflare).
func (z *ZoneID) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if raw == "" || raw == "null" {
		*z = ""
		return nil
	}
	*z = ZoneID(raw)
	return nil
}

// MarshalJSON writes plain IDs as strings and numeric IDs as numbers so the
// Hetzner API keeps receiving the format it expects.
func (z ZoneID) MarshalJSON() ([]byte, error) {
	if n, err := strconv.ParseInt(string(z), 10, 64); err == nil {
		return json.Marshal(n)
	}
	return json.Marshal(string(z))
}

type Zone struct {
	ID        ZoneID    `json:"id"`
	Name      string    `json:"name"`
	Created   time.Time `json:"created"`
	TTL       int       `json:"ttl"`
	Status    string    `json:"status"`
	Registrar string    `json:"registrar"`
	Mode      string    `json:"mode"`
}

type RRSet struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	TTL     int           `json:"ttl"`
	Records []RecordValue `json:"records"`
	Zone    ZoneID        `json:"zone"`
	// Proxied reports whether the record is routed through the provider's
	// proxy. It is nil for providers that do not support proxying.
	Proxied *bool `json:"proxied,omitempty"`
}

type RecordValue struct {
	Value   string `json:"value"`
	Comment string `json:"comment"`
}

// CreateOptions carries provider specific options for record creation.
// Providers that do not understand an option simply ignore it.
type CreateOptions struct {
	// Proxied enables Cloudflare's proxying for a newly created record.
	// A nil value means "use the provider default"; false explicitly asks
	// for a DNS-only record. It is never applied to existing records.
	Proxied *bool
}

type Meta struct {
	Pagination struct {
		Page         int `json:"page"`
		PerPage      int `json:"per_page"`
		PreviousPage int `json:"previous_page"`
		NextPage     int `json:"next_page"`
		LastPage     int `json:"last_page"`
		TotalEntries int `json:"total_entries"`
	} `json:"pagination"`
}

type zonesResponse struct {
	Zones []Zone `json:"zones"`
	Meta  Meta   `json:"meta"`
}

type rrsetsResponse struct {
	RRSets []RRSet `json:"rrsets"`
	Meta   Meta    `json:"meta"`
}

type CreateZoneRequest struct {
	Name string `json:"name"`
	TTL  int    `json:"ttl,omitempty"`
}

type createZoneResponse struct {
	Zone Zone `json:"zone"`
}

// setRecordsRequest is the body for the rrset "set_records" action,
// which is Hetzner's current way of replacing or creating the records
// of an rrset. It supersedes the older PUT /rrsets/{name}/{type} endpoint.
type setRecordsRequest struct {
	Records []RecordValue `json:"records"`
	TTL     *int          `json:"ttl,omitempty"`
}

// Action represents a Hetzner Cloud API async action.
type Action struct {
	ID       int        `json:"id"`
	Status   string     `json:"status"` // "running", "success", "error"
	Command  string     `json:"command"`
	Progress int        `json:"progress"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished"`
	Error    *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// actionResponse wraps a single Action returned by the Hetzner API.
type actionResponse struct {
	Action Action `json:"action"`
}
