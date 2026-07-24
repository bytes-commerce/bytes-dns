package dns

import "time"

type Zone struct {
	ID        int       `json:"id"`
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
	Zone    int           `json:"zone"`
}

type RecordValue struct {
	Value   string `json:"value"`
	Comment string `json:"comment"`
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
	TTL     *int           `json:"ttl,omitempty"`
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
