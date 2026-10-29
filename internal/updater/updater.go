package updater

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/bytes-commerce/bytes-dns/internal/cloudflare"
	"github.com/bytes-commerce/bytes-dns/internal/config"
	"github.com/bytes-commerce/bytes-dns/internal/dns"
	"github.com/bytes-commerce/bytes-dns/internal/ip"
	"github.com/bytes-commerce/bytes-dns/internal/logger"
	"github.com/bytes-commerce/bytes-dns/internal/state"
)

type Result struct {
	PublicIP string
	RecordID string
	ZoneID   string
	Provider string
	Action   Action
	DryRun   bool
}

type Action string

const (
	ActionNoChange Action = "no_change"
	ActionUpdated  Action = "updated"
	ActionCreated  Action = "created"
	ActionSkipped  Action = "skipped"
)

// Options describes the updater that should be built.
type Options struct {
	// Config is the loaded configuration. It is used for the dry run flag
	// and to persist resolved zone IDs.
	Config *config.Config
	// State is the local state manager.
	State *state.Manager
	// Provider describes the provider this updater synchronises.
	Provider config.ProviderConfig
	// Client is the API client of the provider.
	Client dns.Provider
	// Detector detects the public IP. It may be shared between providers so
	// the address is only looked up once per run.
	Detector *ip.Detector
}

type Updater struct {
	cfg          *config.Config
	prov         config.ProviderConfig
	dnsClient    dns.Provider
	ipDetector   *ip.Detector
	stateManager *state.Manager
}

// New returns an updater for the primary provider using the Hetzner API.
func New(cfg *config.Config, sm *state.Manager) *Updater {
	return NewWithDNSClient(cfg, sm, dns.New(cfg.APIToken))
}

// NewWithDNSClient returns an updater for the primary provider with an
// injected client. It exists so tests can supply a mock.
func NewWithDNSClient(cfg *config.Config, sm *state.Manager, client dns.Provider) *Updater {
	return NewForProvider(Options{
		Config:   cfg,
		State:    sm,
		Provider: cfg.PrimaryProvider(),
		Client:   client,
	})
}

// NewForProvider returns an updater bound to a single provider.
func NewForProvider(opts Options) *Updater {
	detector := opts.Detector
	if detector == nil {
		detector = ip.NewWithSources(opts.Config.IPSources())
	}

	return &Updater{
		cfg:          opts.Config,
		prov:         opts.Provider,
		dnsClient:    opts.Client,
		ipDetector:   detector,
		stateManager: opts.State,
	}
}

// NewClientForProvider builds the API client for the given provider.
func NewClientForProvider(prov config.ProviderConfig) (dns.Provider, error) {
	switch prov.Key {
	case config.ProviderHetzner:
		return dns.New(prov.APIToken), nil
	case config.ProviderCloudflare:
		return cloudflare.New(cloudflare.Options{
			APIToken: prov.APIToken,
			Zone:     prov.Zone,
			ZoneID:   prov.ZoneID,
		}), nil
	default:
		return nil, fmt.Errorf("unknown DNS provider %q", prov.Key)
	}
}

func (u *Updater) Run(ctx context.Context, force bool) (result *Result, err error) {
	st, err := u.stateManager.Load()
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}

	// Remember a failed run so that "bytes-dns status" can show it. The
	// last known IP stays untouched, so the next run still updates.
	defer func() {
		if err != nil {
			_ = u.stateManager.MarkErrorFor(st, u.prov.Key, err)
		}
	}()

	currentIP, err := u.detectIP(ctx)
	if err != nil {
		return nil, err
	}

	ipStr := currentIP.String()
	logger.Info("[%s] detected public IP: %s", u.prov.Key, ipStr)

	if !force && u.stateManager.LastIPFor(st, u.prov.Key) == ipStr {
		logger.Info("[%s] IP unchanged (%s) - no update needed", u.prov.Key, ipStr)
		_ = u.stateManager.MarkCheckedFor(st, u.prov.Key)
		return &Result{PublicIP: ipStr, Provider: u.prov.Key, Action: ActionNoChange}, nil
	}

	if err := u.checkApex(); err != nil {
		return nil, err
	}

	zone, err := u.resolveZone(ctx)
	if err != nil {
		return nil, err
	}

	rrset, err := u.dnsClient.FindRRSet(ctx, zone, u.prov.Record, u.prov.RecordType)
	if err != nil {
		return nil, err
	}

	var currentRecordValue string
	if rrset != nil && len(rrset.Records) > 0 {
		currentRecordValue = rrset.Records[0].Value
	}

	if u.cfg.DryRun {
		if rrset != nil {
			if currentRecordValue == ipStr {
				logger.Info("[%s] [dry-run] record %s %s = %s - no change needed", u.prov.Key, u.prov.RecordType, u.prov.Record, ipStr)
				return &Result{PublicIP: ipStr, RecordID: rrset.ID, ZoneID: string(zone.ID), Provider: u.prov.Key, Action: ActionNoChange, DryRun: true}, nil
			}
			logger.Info("[%s] [dry-run] would update record %s %s: %s => %s", u.prov.Key, u.prov.RecordType, u.prov.Record, currentRecordValue, ipStr)
			return &Result{PublicIP: ipStr, RecordID: rrset.ID, ZoneID: string(zone.ID), Provider: u.prov.Key, Action: ActionUpdated, DryRun: true}, nil
		}
		logger.Info("[%s] [dry-run] would create record %s %s = %s", u.prov.Key, u.prov.RecordType, u.prov.Record, ipStr)
		return &Result{PublicIP: ipStr, ZoneID: string(zone.ID), Provider: u.prov.Key, Action: ActionCreated, DryRun: true}, nil
	}

	if rrset != nil {
		if currentRecordValue == ipStr {
			logger.Info("[%s] DNS record already correct (%s %s = %s) - no API write needed", u.prov.Key, u.prov.RecordType, u.prov.Record, ipStr)
			_ = u.stateManager.MarkUpdatedFor(st, u.prov.Key, ipStr, rrset.ID)
			return &Result{PublicIP: ipStr, RecordID: rrset.ID, ZoneID: string(zone.ID), Provider: u.prov.Key, Action: ActionNoChange}, nil
		}

		logger.Info("[%s] updating %s record %s: %s => %s", u.prov.Key, u.prov.RecordType, u.prov.Record, currentRecordValue, ipStr)
		updated, err := u.dnsClient.UpdateRRSet(ctx, zone, rrset, ipStr)
		if err != nil {
			return nil, fmt.Errorf("DNS update failed: %w", err)
		}
		_ = u.stateManager.MarkUpdatedFor(st, u.prov.Key, ipStr, updated.ID)
		logger.Info("[%s] record updated successfully (id=%s)", u.prov.Key, updated.ID)
		return &Result{PublicIP: ipStr, RecordID: updated.ID, ZoneID: string(zone.ID), Provider: u.prov.Key, Action: ActionUpdated}, nil
	}

	logger.Info("[%s] creating %s record %s = %s (ttl=%d)", u.prov.Key, u.prov.RecordType, u.prov.Record, ipStr, u.prov.TTL)
	created, err := u.dnsClient.CreateRRSet(ctx, zone, u.prov.Record, u.prov.RecordType, ipStr, u.prov.TTL, &dns.CreateOptions{Proxied: u.prov.Proxied})
	if err != nil {
		return nil, fmt.Errorf("DNS create failed: %w", err)
	}
	_ = u.stateManager.MarkUpdatedFor(st, u.prov.Key, ipStr, created.ID)
	logger.Info("[%s] record created successfully (id=%s)", u.prov.Key, created.ID)
	return &Result{PublicIP: ipStr, RecordID: created.ID, ZoneID: string(zone.ID), Provider: u.prov.Key, Action: ActionCreated}, nil
}

func (u *Updater) Test(ctx context.Context) error {
	fmt.Printf("=== bytes-dns connection test (%s) ===\n", u.prov.Key)

	currentIP, err := u.detectIP(ctx)
	if err != nil {
		return fmt.Errorf("IP detection failed: %w", err)
	}
	fmt.Printf("  public IP  : %s\n", currentIP)

	if err := u.checkApex(); err != nil {
		return err
	}

	zone, err := u.resolveZone(ctx)
	if err != nil {
		return fmt.Errorf("zone lookup failed: %w", err)
	}
	fmt.Printf("  zone       : %s (id=%s)\n", zone.Name, zone.ID)

	fmt.Printf("  record     : %s %s\n", u.prov.RecordType, u.prov.Record)

	rrset, err := u.dnsClient.FindRRSet(ctx, zone, u.prov.Record, u.prov.RecordType)
	if err != nil {
		return fmt.Errorf("record lookup failed: %w", err)
	}

	var currentRecordValue string
	if rrset != nil && len(rrset.Records) > 0 {
		currentRecordValue = rrset.Records[0].Value
	}

	if rrset != nil && rrset.Proxied != nil {
		fmt.Printf("  proxied    : %v\n", *rrset.Proxied)
	}

	if rrset == nil {
		fmt.Printf("  status     : record does not exist - would CREATE with value=%s\n", currentIP)
	} else if currentRecordValue == currentIP.String() {
		fmt.Printf("  status     : record exists with correct value=%s - no change needed\n", currentRecordValue)
	} else {
		fmt.Printf("  status     : record exists with value=%s - would UPDATE to %s\n", currentRecordValue, currentIP)
	}

	fmt.Println("=== test passed ===")
	return nil
}

// checkApex refuses to work on the apex record of a zone.
func (u *Updater) checkApex() error {
	record := strings.TrimRight(u.prov.Record, ".")
	zone := strings.TrimRight(u.prov.Zone, ".")
	if zone != "" && strings.EqualFold(record, zone) {
		return fmt.Errorf("refusing to operate on zone apex record %q - use a subdomain instead (e.g. \"home.%s\")", u.prov.Record, u.prov.Zone)
	}
	return nil
}

// resolveZone returns the zone of the provider, either from the cached zone
// ID or by looking it up. A freshly resolved zone is written back to the
// configuration.
func (u *Updater) resolveZone(ctx context.Context) (*dns.Zone, error) {
	if u.prov.ZoneID != "" {
		return &dns.Zone{ID: dns.ZoneID(u.prov.ZoneID), Name: u.prov.Zone}, nil
	}

	resolved, err := u.dnsClient.FindZoneByRecord(ctx, u.prov.Record)
	if err != nil || resolved == nil {
		resolved, err = u.dnsClient.FindZone(ctx, u.prov.Zone)
		if err != nil {
			return nil, err
		}
	} else if u.prov.Zone != "" && !strings.EqualFold(resolved.Name, u.prov.Zone) {
		// Suffix match disagrees with the configured zone - fall back to the explicit one.
		resolved, err = u.dnsClient.FindZone(ctx, u.prov.Zone)
		if err != nil {
			return nil, err
		}
	}

	// Only persist the config if the resolved zone is actually new.
	if u.prov.ZoneID != string(resolved.ID) || !strings.EqualFold(u.prov.Zone, resolved.Name) {
		u.prov.ZoneID = string(resolved.ID)
		u.prov.Zone = resolved.Name
		if err := u.cfg.SetZone(u.prov.Key, u.prov.ZoneID, u.prov.Zone); err != nil {
			logger.Warn("failed to save resolved zone_id: %v", err)
		}
	}
	logger.Debug("[%s] resolved zone %q => id=%s", u.prov.Key, u.prov.Zone, u.prov.ZoneID)

	return resolved, nil
}

func (u *Updater) detectIP(ctx context.Context) (net.IP, error) {
	var currentIP net.IP
	var err error

	switch u.prov.RecordType {
	case "A":
		currentIP, err = u.ipDetector.DetectIPv4(ctx)
	case "AAAA":
		currentIP, err = u.ipDetector.DetectIPv6(ctx)
	default:
		return nil, fmt.Errorf("unsupported record_type: %s", u.prov.RecordType)
	}

	if err != nil {
		return nil, fmt.Errorf("public IP detection failed: %w", err)
	}

	if !u.cfg.AllowPrivateIP && config.IsPrivateIP(currentIP) {
		return nil, fmt.Errorf(
			"detected IP %s is a private/RFC1918 address - if this is intentional, set allow_private_ip=true in config",
			currentIP,
		)
	}

	return currentIP, nil
}
