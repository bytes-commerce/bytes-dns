package dns

import "time"

type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Record struct {
	ID       string `json:"id"`
	ZoneID   string `json:"zone_id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	TTL      int    `json:"ttl"`
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
}

type zonesResponse struct {
	Zones []Zone `json:"zones"`
	Meta  struct {
		Pagination struct {
			Page         int `json:"page"`
			PerPage      int `json:"per_page"`
			LastPage     int `json:"last_page"`
			TotalEntries int `json:"total_entries"`
		} `json:"pagination"`
	} `json:"meta"`
}

type recordsResponse struct {
	Records []Record `json:"records"`
}

type recordResponse struct {
	Record Record `json:"record"`
}

type createRecordRequest struct {
	ZoneID string `json:"zone_id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	TTL    int    `json:"ttl"`
}

type updateRecordRequest struct {
	ZoneID string `json:"zone_id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	TTL    int    `json:"ttl"`
}
