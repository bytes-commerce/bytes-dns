package ip

import (
	"context"
	"errors"
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
	sources []string
	client  *http.Client
}

func New(source string) *Detector {
	return NewWithSources([]string{source})
}

func NewWithSources(sources []string) *Detector {
	cleaned := make([]string, 0, len(sources))
	for _, s := range sources {
		s = strings.TrimSpace(s)
		if s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		cleaned = []string{"https://api4.my-ip.io/ip.txt"}
	}
	return &Detector{
		sources: cleaned,
		client: &http.Client{
			Timeout: requestTimeout,
		},
	}
}

// Sources returns the configured IP detection URLs in priority order.
func (d *Detector) Sources() []string {
	out := make([]string, len(d.sources))
	copy(out, d.sources)
	return out
}

func (d *Detector) DetectIPv4(ctx context.Context) (net.IP, error) {
	var errs []error
	for _, src := range d.sources {
		raw, err := d.fetchFrom(ctx, src)
		if err == nil {
			parsed := net.ParseIP(raw)
			switch {
			case parsed == nil:
				err = fmt.Errorf("ip source returned non-IP value %q", raw)
			case parsed.To4() == nil:
				err = fmt.Errorf("ip source returned IPv6 address %q but record_type A requires IPv4", raw)
			default:
				return parsed.To4(), nil
			}
		}
		errs = append(errs, fmt.Errorf("%s: %w", src, err))
	}
	return nil, allSourcesFailed(errs)
}

func (d *Detector) DetectIPv6(ctx context.Context) (net.IP, error) {
	var errs []error
	for _, src := range d.sources {
		raw, err := d.fetchFrom(ctx, src)
		if err == nil {
			parsed := net.ParseIP(raw)
			switch {
			case parsed == nil:
				err = fmt.Errorf("ip source returned non-IP value %q", raw)
			case parsed.To4() != nil:
				err = fmt.Errorf("ip source returned IPv4 address %q but record_type AAAA requires IPv6", raw)
			default:
				return parsed, nil
			}
		}
		errs = append(errs, fmt.Errorf("%s: %w", src, err))
	}
	return nil, allSourcesFailed(errs)
}

func allSourcesFailed(errs []error) error {
	if len(errs) == 0 {
		return errors.New("no ip sources configured")
	}
	return fmt.Errorf("all ip sources failed: %w", errors.Join(errs...))
}

func (d *Detector) fetchFrom(ctx context.Context, source string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", fmt.Errorf("cannot build request: %w", err)
	}
	req.Header.Set("User-Agent", "bytes-dns/1.0 (+https://github.com/bytes-commerce/bytes-dns)")
	req.Header.Set("Accept", "text/plain")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ip detection request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ip source returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return "", fmt.Errorf("ip source returned an empty response")
	}
	return raw, nil
}
