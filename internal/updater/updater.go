package updater

import (
	"context"
	"fmt"
	"net"

	"github.com/bytesbytes/bytes-dns/internal/config"
	"github.com/bytesbytes/bytes-dns/internal/dns"
	"github.com/bytesbytes/bytes-dns/internal/ip"
	"github.com/bytesbytes/bytes-dns/internal/logger"
	"github.com/bytesbytes/bytes-dns/internal/state"
)

type dnsClient interface {
	FindZone(ctx context.Context, zoneName string) (*dns.Zone, error)
	FindRecord(ctx context.Context, zoneID, name, recordType string) (*dns.Record, error)
	UpdateRecord(ctx context.Context, record *dns.Record, newValue string, ttl int) (*dns.Record, error)
	CreateRecord(ctx context.Context, zoneID, name, recordType, value string, ttl int) (*dns.Record, error)
}

type Result struct {
	PublicIP   string
	RecordID   string
	ZoneID     string
	Action     Action
	DryRun     bool
}

type Action string

const (
	ActionNoChange Action = "no_change"
	ActionUpdated  Action = "updated"
	ActionCreated  Action = "created"
	ActionSkipped  Action = "skipped"
)

type Updater struct {
	cfg          *config.Config
	dnsClient    dnsClient
	ipDetector   *ip.Detector
	stateManager *state.Manager
}

func New(cfg *config.Config, sm *state.Manager) *Updater {
	return NewWithDNSClient(cfg, sm, dns.New(cfg.APIToken))
}

func NewWithDNSClient(cfg *config.Config, sm *state.Manager, client dnsClient) *Updater {
	return &Updater{
		cfg:          cfg,
		dnsClient:    client,
		ipDetector:   ip.New(cfg.IPSource),
		stateManager: sm,
	}
}

func (u *Updater) Run(ctx context.Context, force bool) (*Result, error) {
	st, err := u.stateManager.Load()
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}

	currentIP, err := u.detectIP(ctx)
	if err != nil {
		return nil, err
	}

	ipStr := currentIP.String()
	logger.Info("detected public IP: %s", ipStr)

	if !force && st.LastIP == ipStr {
		logger.Info("IP unchanged (%s) — no update needed", ipStr)
		_ = u.stateManager.MarkChecked(st)
		return &Result{PublicIP: ipStr, Action: ActionNoChange}, nil
	}

	zone, err := u.dnsClient.FindZone(ctx, u.cfg.Zone)
	if err != nil {
		return nil, err
	}
	logger.Debug("resolved zone %q => id=%s", u.cfg.Zone, zone.ID)

	label := u.cfg.RecordLabel()
	logger.Debug("record label: %q (record=%s, zone=%s)", label, u.cfg.Record, u.cfg.Zone)

	record, err := u.dnsClient.FindRecord(ctx, zone.ID, label, u.cfg.RecordType)
	if err != nil {
		return nil, err
	}

	if u.cfg.DryRun {
		if record != nil {
			if record.Value == ipStr {
				logger.Info("[dry-run] record %s %s = %s — no change needed", u.cfg.RecordType, u.cfg.Record, ipStr)
				return &Result{PublicIP: ipStr, RecordID: record.ID, ZoneID: zone.ID, Action: ActionNoChange, DryRun: true}, nil
			}
			logger.Info("[dry-run] would update record %s %s: %s => %s", u.cfg.RecordType, u.cfg.Record, record.Value, ipStr)
			return &Result{PublicIP: ipStr, RecordID: record.ID, ZoneID: zone.ID, Action: ActionUpdated, DryRun: true}, nil
		}
		logger.Info("[dry-run] would create record %s %s = %s", u.cfg.RecordType, u.cfg.Record, ipStr)
		return &Result{PublicIP: ipStr, ZoneID: zone.ID, Action: ActionCreated, DryRun: true}, nil
	}

	if record != nil {
		if record.Value == ipStr {
			logger.Info("DNS record already correct (%s %s = %s) — no API write needed", u.cfg.RecordType, u.cfg.Record, ipStr)
			_ = u.stateManager.MarkUpdated(st, ipStr, record.ID)
			return &Result{PublicIP: ipStr, RecordID: record.ID, ZoneID: zone.ID, Action: ActionNoChange}, nil
		}

		logger.Info("updating %s record %s: %s => %s", u.cfg.RecordType, u.cfg.Record, record.Value, ipStr)
		updated, err := u.dnsClient.UpdateRecord(ctx, record, ipStr, u.cfg.TTL)
		if err != nil {
			return nil, fmt.Errorf("DNS update failed: %w", err)
		}
		_ = u.stateManager.MarkUpdated(st, ipStr, updated.ID)
		logger.Info("record updated successfully (id=%s)", updated.ID)
		return &Result{PublicIP: ipStr, RecordID: updated.ID, ZoneID: zone.ID, Action: ActionUpdated}, nil
	}

	logger.Info("creating %s record %s = %s (ttl=%d)", u.cfg.RecordType, u.cfg.Record, ipStr, u.cfg.TTL)
	created, err := u.dnsClient.CreateRecord(ctx, zone.ID, label, u.cfg.RecordType, ipStr, u.cfg.TTL)
	if err != nil {
		return nil, fmt.Errorf("DNS create failed: %w", err)
	}
	_ = u.stateManager.MarkUpdated(st, ipStr, created.ID)
	logger.Info("record created successfully (id=%s)", created.ID)
	return &Result{PublicIP: ipStr, RecordID: created.ID, ZoneID: zone.ID, Action: ActionCreated}, nil
}

func (u *Updater) Test(ctx context.Context) error {
	fmt.Println("=== bytes-dns connection test ===")

	currentIP, err := u.detectIP(ctx)
	if err != nil {
		return fmt.Errorf("IP detection failed: %w", err)
	}
	fmt.Printf("  public IP  : %s\n", currentIP)

	zone, err := u.dnsClient.FindZone(ctx, u.cfg.Zone)
	if err != nil {
		return fmt.Errorf("zone lookup failed: %w", err)
	}
	fmt.Printf("  zone       : %s (id=%s)\n", zone.Name, zone.ID)

	label := u.cfg.RecordLabel()
	fmt.Printf("  record     : %s %s (label=%q)\n", u.cfg.RecordType, u.cfg.Record, label)

	record, err := u.dnsClient.FindRecord(ctx, zone.ID, label, u.cfg.RecordType)
	if err != nil {
		return fmt.Errorf("record lookup failed: %w", err)
	}

	if record == nil {
		fmt.Printf("  status     : record does not exist — would CREATE with value=%s\n", currentIP)
	} else if record.Value == currentIP.String() {
		fmt.Printf("  status     : record exists with correct value=%s — no change needed\n", record.Value)
	} else {
		fmt.Printf("  status     : record exists with value=%s — would UPDATE to %s\n", record.Value, currentIP)
	}

	fmt.Println("=== test passed ===")
	return nil
}

func (u *Updater) detectIP(ctx context.Context) (net.IP, error) {
	var (
		currentIP net.IP
		err       error
	)

	switch u.cfg.RecordType {
	case "A":
		currentIP, err = u.ipDetector.DetectIPv4(ctx)
	case "AAAA":
		currentIP, err = u.ipDetector.DetectIPv6(ctx)
	default:
		return nil, fmt.Errorf("unsupported record_type: %s", u.cfg.RecordType)
	}

	if err != nil {
		return nil, fmt.Errorf("public IP detection failed: %w", err)
	}

	if !u.cfg.AllowPrivateIP && config.IsPrivateIP(currentIP) {
		return nil, fmt.Errorf(
			"detected IP %s is a private/RFC1918 address — if this is intentional, set allow_private_ip=true in config",
			currentIP,
		)
	}

	return currentIP, nil
}
