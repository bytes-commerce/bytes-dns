package dns

import "context"

// Provider is the contract every DNS backend has to fulfil. It is
// deliberately expressed in provider neutral terms: zones are addressed by
// their opaque ID and records by their fully qualified name, so each
// implementation can map that onto its own naming rules.
type Provider interface {
	// FindZone returns the zone with the given name.
	FindZone(ctx context.Context, zoneName string) (*Zone, error)
	// FindZoneByRecord returns the zone that hosts the given fully qualified
	// record name.
	FindZoneByRecord(ctx context.Context, fqdn string) (*Zone, error)
	// FindRRSet returns the record set for fqdn and recordType, or nil when
	// no such record exists.
	FindRRSet(ctx context.Context, zone *Zone, fqdn, recordType string) (*RRSet, error)
	// UpdateRRSet replaces the value of an existing record set and returns
	// the updated record set.
	UpdateRRSet(ctx context.Context, zone *Zone, rrset *RRSet, newValue string) (*RRSet, error)
	// CreateRRSet creates a record set with the given value and returns it.
	CreateRRSet(ctx context.Context, zone *Zone, fqdn, recordType, value string, ttl int, opts *CreateOptions) (*RRSet, error)
}
