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

type rrsetResponse struct {
	RRSet RRSet `json:"rrset"`
}

type createRRSetRequest struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	TTL     int               `json:"ttl"`
	Records []RecordValue     `json:"records"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type updateRRSetRequest struct {
	Records []RecordValue `json:"records"`
}
