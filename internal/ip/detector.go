package ip

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseBody = 64
)

type Detector struct {
	source string
	client *http.Client
}

func New(source string) *Detector {
	return &Detector{
		source: source,
		client: &http.Client{
			Timeout: requestTimeout,
		},
	}
}

func (d *Detector) DetectIPv4(ctx context.Context) (net.IP, error) {
	raw, err := d.fetch(ctx)
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("ip source returned non-IP value %q (source: %s)", raw, d.source)
	}

	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("ip source returned IPv6 address %q but record_type A requires IPv4 (source: %s)", raw, d.source)
	}

	return v4, nil
}

func (d *Detector) DetectIPv6(ctx context.Context) (net.IP, error) {
	raw, err := d.fetch(ctx)
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("ip source returned non-IP value %q (source: %s)", raw, d.source)
	}

	if ip.To4() != nil {
		return nil, fmt.Errorf("ip source returned IPv4 address %q but record_type AAAA requires IPv6 (source: %s)", raw, d.source)
	}

	return ip, nil
}

func (d *Detector) fetch(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.source, nil)
	if err != nil {
		return "", fmt.Errorf("cannot build request to %s: %w", d.source, err)
	}
	req.Header.Set("User-Agent", "bytes-dns/1.0 (+https://github.com/bytes-commerce/bytes-dns)")
	req.Header.Set("Accept", "text/plain")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ip detection request to %s failed: %w", d.source, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ip source %s returned HTTP %d", d.source, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return "", fmt.Errorf("failed to read response from %s: %w", d.source, err)
	}

	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return "", fmt.Errorf("ip source %s returned an empty response", d.source)
	}

	return raw, nil
}
