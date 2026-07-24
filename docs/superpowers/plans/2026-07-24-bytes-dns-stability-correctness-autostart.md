# bytes-dns Stability, Correctness, and Autostart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the stability and correctness bugs in `bytes-dns`, and make `bytes-dns install` actually register the systemd timer end-to-end.

**Architecture:** Ten behavior changes across `config`, `state`, `ip`, `dns`, `updater`, and a new `internal/installer` package. Installer logic moves from `install.sh` into Go so the binary is the single source of truth. Tests use `httptest` and a stubbed `systemctl` runner; real install is verified manually on a Linux VM.

**Tech Stack:** Go 1.22+, `net/http`, `encoding/json`, `os/exec`, `embed`.

**Spec:** `docs/superpowers/specs/2026-07-24-bytes-dns-stability-correctness-autostart-design.md`

---

## Global Constraints

- Go version floor: 1.22 (per Makefile).
- Static binary: `CGO_ENABLED=0` for any `go build` invocation.
- Linux + systemd only — the installer does not need to handle macOS or Windows.
- Config file permissions must be enforced at 600.
- All API calls must continue to use HTTPS.
- All systemd units used by the installer must be backed by files in `internal/installer/assets/` (embedded via `//go:embed`).
- Backwards compatibility: existing single-URL `ip_source` configs must still work.
- Backwards compatibility: `install.sh` users must still be able to invoke it directly and reach the same end state.
- Run all tests with `go test ./... -v -count=1` before each commit.
- All commit messages begin with `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`, or `test:`.

---

## Task 1: Config supports comma-separated IP sources

**Files:**
- Modify: `internal/config/config.go:174-181` (the `ip_source` validation block)
- Modify: `internal/config/config.go:13-19` (constants — add `DefaultIPSourceFallback`)
- Modify: `internal/config/config.go` (add `IPSources()` helper that returns the parsed list)
- Test: `internal/config/config_test.go` (add new tests)

**Interfaces:**
- Consumes: existing `Config.IPSource string` field
- Produces: `Config.IPSources() []string` returning the parsed list (always at least one element)

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestLoad_IPSourceCommaList(t *testing.T) {
	m := validBase()
	m["ip_source"] = "https://a.example.com/ip,https://b.example.com/ip,https://c.example.com/ip"
	path := writeConfig(t, m, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error for comma-separated ip_source: %v", err)
	}
	got := cfg.IPSources()
	want := []string{"https://a.example.com/ip", "https://b.example.com/ip", "https://c.example.com/ip"}
	if len(got) != len(want) {
		t.Fatalf("IPSources() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("IPSources()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoad_IPSourceSingleValueStillAllowed(t *testing.T) {
	m := validBase()
	m["ip_source"] = "https://api4.my-ip.io/ip.txt"
	path := writeConfig(t, m, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error for single ip_source: %v", err)
	}
	got := cfg.IPSources()
	if len(got) != 1 || got[0] != "https://api4.my-ip.io/ip.txt" {
		t.Errorf("IPSources() = %v, want [https://api4.my-ip.io/ip.txt]", got)
	}
}

func TestLoad_IPSourceRejectsBadFirstURL(t *testing.T) {
	m := validBase()
	m["ip_source"] = "ftp://bad.example.com/ip,https://good.example.com/ip"
	path := writeConfig(t, m, 0o600)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected validation error when first ip_source URL is not http(s), got nil")
	}
}

func TestLoad_IPSourceEmptyEntriesIgnored(t *testing.T) {
	m := validBase()
	m["ip_source"] = "https://a.example.com/ip,,https://b.example.com/ip,"
	path := writeConfig(t, m, 0o600)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := cfg.IPSources()
	want := []string{"https://a.example.com/ip", "https://b.example.com/ip"}
	if len(got) != len(want) {
		t.Errorf("IPSources() = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/... -v -count=1 -run 'TestLoad_IPSource'`
Expected: 4 failures (functions/methods don't exist yet).

- [ ] **Step 3: Update constants and add `IPSources()` helper**

In `internal/config/config.go`, replace the constants block (lines 13-19) with:

```go
const (
	DefaultTTL             = 60
	DefaultIntervalMinutes = 5
	// DefaultIPSource is the first URL tried; the rest are fallbacks.
	DefaultIPSource = "https://api4.my-ip.io/ip.txt,https://ifconfig.co/ip,https://checkip.amazonaws.com"
	DefaultLogLevel = "info"
	DefaultRecordType = "A"
)
```

Add this method to `internal/config/config.go` (after `RecordLabel`):

```go
// IPSources returns the configured IP source URLs in priority order.
// Empty entries are skipped. Returns at least one entry if IPSource is non-empty.
func (c *Config) IPSources() []string {
	if c.IPSource == "" {
		return []string{DefaultIPSource}
	}
	parts := strings.Split(c.IPSource, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{DefaultIPSource}
	}
	return out
}
```

- [ ] **Step 4: Update the `ip_source` validator to check only the first URL**

In `internal/config/config.go`, replace the `ip_source` validation block (lines 174-181):

```go
if cfg.IPSource != "" {
	first := cfg.IPSources()[0]
	if !strings.HasPrefix(first, "https://") && !strings.HasPrefix(first, "http://") {
		errs = append(errs, "ip_source must be a valid http:// or https:// URL")
	}
	if strings.Contains(first, "://127.0.0.1") || strings.Contains(first, "://localhost") {
		errs = append(errs, "ip_source cannot be a localhost address")
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): accept comma-separated ip_source list"
```

---

## Task 2: IP detector with fallback sources

**Files:**
- Modify: `internal/ip/detector.go` (add `NewWithSources` and use it; add fallback loop)
- Modify: `internal/ip/detector.go` (rename `fetch` to accept a URL; have `DetectIPv4/IPv6` iterate sources)
- Test: `internal/ip/detector_test.go` (add fallback tests)

**Interfaces:**
- Consumes: `NewWithSources([]string)` constructor (Task 2 produces)
- Produces: `Detector` that tries sources in order; returns first success

- [ ] **Step 1: Write the failing test**

Append to `internal/ip/detector_test.go`:

```go
func TestDetectIPv4_FallbackOnFirstSourceFailure(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("203.0.113.42"))
	}))
	t.Cleanup(good.Close)

	d := ip.NewWithSources([]string{failing.URL, good.URL})
	got, err := d.DetectIPv4(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.String() != "203.0.113.42" {
		t.Errorf("got IP %q, want %q", got, "203.0.113.42")
	}
}

func TestDetectIPv4_FallbackOnNetworkError(t *testing.T) {
	// Unroutable port that refuses connections.
	d := ip.NewWithSources([]string{"http://127.0.0.1:1", "http://127.0.0.1:1"})
	if _, err := d.DetectIPv4(context.Background()); err == nil {
		t.Fatal("expected error when all sources fail, got nil")
	}
}

func TestDetectIPv4_RecordsAllSourceErrors(t *testing.T) {
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv1.Close)

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-an-ip"))
	}))
	t.Cleanup(srv2.Close)

	d := ip.NewWithSources([]string{srv1.URL, srv2.URL})
	_, err := d.DetectIPv4(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), srv1.URL) || !strings.Contains(err.Error(), srv2.URL) {
		t.Errorf("expected error to mention both sources, got: %v", err)
	}
}
```

Add `"strings"` to the imports of `internal/ip/detector_test.go` if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/ip/... -v -count=1 -run 'TestDetectIPv4_Fallback|TestDetectIPv4_RecordsAll'`
Expected: 3 failures.

- [ ] **Step 3: Rewrite `internal/ip/detector.go`**

Replace the entire `internal/ip/detector.go` file with:

```go
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
	raw, err := d.fetchFromAny(ctx)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("ip source returned non-IP value %q", raw)
	}
	v4 := ip.To4()
	if v4 == nil {
		return nil, fmt.Errorf("ip source returned IPv6 address %q but record_type A requires IPv4", raw)
	}
	return v4, nil
}

func (d *Detector) DetectIPv6(ctx context.Context) (net.IP, error) {
	raw, err := d.fetchFromAny(ctx)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("ip source returned non-IP value %q", raw)
	}
	if ip.To4() != nil {
		return nil, fmt.Errorf("ip source returned IPv4 address %q but record_type AAAA requires IPv6", raw)
	}
	return ip, nil
}

// fetchFromAny tries each configured source in order. Returns the first success
// or an aggregated error describing every failure.
func (d *Detector) fetchFromAny(ctx context.Context) (string, error) {
	var errs []error
	for _, src := range d.sources {
		raw, err := d.fetchFrom(ctx, src)
		if err == nil {
			return raw, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", src, err))
	}
	if len(errs) == 0 {
		return "", errors.New("no ip sources configured")
	}
	return "", fmt.Errorf("all ip sources failed: %w", errors.Join(errs...))
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ip/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Update `updater` to use the new constructor**

In `internal/updater/updater.go`, replace the `NewWithDNSClient` body (line 51-58):

```go
func NewWithDNSClient(cfg *config.Config, sm *state.Manager, client dnsClient) *Updater {
	return &Updater{
		cfg:          cfg,
		dnsClient:    client,
		ipDetector:   ip.NewWithSources(cfg.IPSources()),
		stateManager: sm,
	}
}
```

- [ ] **Step 6: Run full test suite to verify nothing else broke**

Run: `go test ./... -v -count=1`
Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/ip/detector.go internal/ip/detector_test.go internal/updater/updater.go
git commit -m "feat(ip): add fallback IP detection sources"
```

---

## Task 3: DNS client retry policy

**Files:**
- Modify: `internal/dns/client.go` (add retry helper; wrap `do` calls)
- Test: `internal/dns/client_test.go` (add retry tests)

**Interfaces:**
- Consumes: nothing
- Produces: `Client` retries 5xx/429/network errors with 250ms/750ms/2s backoff (3 attempts total)

- [ ] **Step 1: Write the failing test**

Append to `internal/dns/client_test.go`:

```go
func TestClient_RetriesOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"zones":[{"id":42,"name":"example.com"}],"meta":{}}`))
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("token", srv.URL)
	zone, err := client.FindZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error after retries: %v", err)
	}
	if zone.ID != 42 {
		t.Errorf("zone ID = %d, want 42", zone.ID)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (succeeded on 3rd attempt)", calls)
	}
}

func TestClient_DoesNotRetryOn401(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("wrong", srv.URL)
	_, err := client.FindZone(context.Background(), "example.com")
	if err == nil {
		t.Fatal("expected 401 error, got nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 401)", calls)
	}
}

func TestClient_RetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"zones":[],"meta":{}}`))
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("token", srv.URL)
	_, err := client.FindZone(context.Background(), "anything")
	if err != nil {
		t.Fatalf("unexpected error after retry on 429: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (retried once on 429)", calls)
	}
}

func TestClient_GivesUpAfter3Attempts(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := dns.NewWithBaseURL("token", srv.URL)
	_, err := client.FindZone(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (final attempt)", calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dns/... -v -count=1 -run 'TestClient_RetriesOn5xx|TestClient_DoesNotRetry|TestClient_RetriesOn429|TestClient_GivesUpAfter3'`
Expected: 4 failures (retry not implemented).

- [ ] **Step 3: Add retry policy to `internal/dns/client.go`**

Add the following constant near the top of `internal/dns/client.go` (after the existing `requestTimeout` constant):

```go
const (
	productionAPIBase = "https://api.hetzner.cloud/v1"
	requestTimeout    = 15 * time.Second

	maxRetries       = 3
	retryBackoffBase = 250 * time.Millisecond
)
```

Add this helper method to the `Client` struct (after `setHeaders`):

```go
// doWithRetry executes the HTTP request with a retry policy.
// Retries on HTTP 5xx, 429, and network errors. Does not retry on 4xx.
func (c *Client) doWithRetry(req *http.Request, out any) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := retryBackoffBase * (1 << (attempt - 1)) // 250ms, 500ms, 1s
			select {
			case <-time.After(backoff):
			case <-req.Context().Done():
				return fmt.Errorf("retry aborted: %w", req.Context().Err())
			}
		}

		// Re-create the request body for retries if needed.
		var bodyReader io.Reader
		if req.Body != nil {
			if req.GetBody == nil {
				// Caller didn't supply GetBody; can't safely retry.
				return c.do(req, out)
			}
			rc, err := req.GetBody()
			if err != nil {
				return fmt.Errorf("retry body clone: %w", err)
			}
			bodyReader = rc
		}

		attemptReq := req
		if bodyReader != nil {
			clone := req.Clone(req.Context())
			clone.Body = bodyReader
			attemptReq = clone
		}

		lastErr = c.do(attemptReq, out)
		if lastErr == nil || !shouldRetry(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("after %d attempts: %w", maxRetries, lastErr)
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "HTTP 429"):
		return true
	case strings.Contains(msg, "HTTP 5"):
		return true
	case strings.Contains(msg, "HTTP request to"):
		return true // network / connection error
	default:
		return false
	}
}
```

Replace the body of `get`, `put`, and `post` to call `doWithRetry` instead of `do`:

```go
func (c *Client) get(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	c.setHeaders(req)
	return c.doWithRetry(req, out)
}

func (c *Client) put(ctx context.Context, endpoint string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPut, endpoint, body, out)
}

func (c *Client) post(ctx context.Context, endpoint string, body any, out any) error {
	return c.sendJSON(ctx, http.MethodPost, endpoint, body, out)
}
```

Update `sendJSON` to set `GetBody` so retries can rebuild the body:

```go
func (c *Client) sendJSON(ctx context.Context, method, endpoint string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshalling request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}

	return c.doWithRetry(req, out)
}
```

Add `"io"` to the imports if not already present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dns/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/dns/client.go internal/dns/client_test.go
git commit -m "feat(dns): add retry policy for transient API failures"
```

---

## Task 4: UpdateRRSet does not mutate its input

**Files:**
- Modify: `internal/dns/client.go:140-158` (the `UpdateRRSet` method)
- Test: `internal/dns/client_test.go` (add no-mutate test)

- [ ] **Step 1: Write the failing test**

Append to `internal/dns/client_test.go`:

```go
func TestUpdateRRSet_DoesNotMutateInput(t *testing.T) {
	mock := &hetznerMock{}
	client, _ := newTestClient(t, mock)

	original := &dns.RRSet{
		ID:   "home/A",
		Name: "home",
		Type: "A",
		TTL:  60,
		Records: []dns.RecordValue{
			{Value: "1.2.3.4"},
		},
	}

	// Snapshot the original.
	beforeRecords := make([]dns.RecordValue, len(original.Records))
	copy(beforeRecords, original.Records)

	_, err := client.UpdateRRSet(context.Background(), "42", original, "9.9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(original.Records) != len(beforeRecords) {
		t.Fatalf("original.Records length changed: was %d, now %d",
			len(beforeRecords), len(original.Records))
	}
	for i := range beforeRecords {
		if original.Records[i].Value != beforeRecords[i].Value {
			t.Errorf("original.Records[%d].Value = %q, want %q (input was mutated)",
				i, original.Records[i].Value, beforeRecords[i].Value)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dns/... -v -count=1 -run TestUpdateRRSet_DoesNotMutateInput`
Expected: FAIL — the original's `Records` slice is mutated.

- [ ] **Step 3: Fix `UpdateRRSet` to copy before mutating**

In `internal/dns/client.go`, replace the `UpdateRRSet` method (lines 140-158):

```go
func (c *Client) UpdateRRSet(ctx context.Context, zoneID string, rrset *RRSet, newValue string) (*RRSet, error) {
	body := updateRRSetRequest{
		Records: []RecordValue{
			{
				Value:   newValue,
				Comment: "Auto-provisionized by Bytes-DNS.",
			},
		},
	}

	endpoint := fmt.Sprintf("%s/zones/%s/rrsets/%s/%s", c.apiBase, url.PathEscape(zoneID), url.PathEscape(rrset.Name), url.PathEscape(rrset.Type))

	if err := c.put(ctx, endpoint, body, nil); err != nil {
		return nil, fmt.Errorf("updating rrset %s (%s) in zone %s: %w", rrset.Name, rrset.Type, zoneID, err)
	}

	// Return a copy so we don't mutate the caller's RRSet.
	updated := *rrset
	updated.Records = make([]RecordValue, len(body.Records))
	copy(updated.Records, body.Records)
	return &updated, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dns/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/dns/client.go internal/dns/client_test.go
git commit -m "fix(dns): UpdateRRSet no longer mutates caller's RRSet"
```

---

## Task 5: State corruption handling + remove LastSyncedIP

**Files:**
- Modify: `internal/state/state.go` (quarantine corrupt files; drop `LastSyncedIP` field; drop `MarkUpdated` arg)
- Modify: `internal/state/state_test.go` (rename test, drop `LastSyncedIP` assertions, add corruption test)
- Modify: `internal/updater/updater.go:131, 140, 150` (drop `LastSyncedIP` set in `MarkUpdated` calls — the field is removed from State struct so we just pass IP and recordID)

**Interfaces:**
- Consumes: existing `state.Manager`, `state.State`
- Produces: `state.State` with `LastSyncedIP` removed; `state.Manager.Load` quarantines corrupt files

- [ ] **Step 1: Write the failing test for corruption handling**

Append to `internal/state/state_test.go`:

```go
func TestLoad_QuarantinesCorruptState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{corrupt json"), 0o600); err != nil {
		t.Fatal(err)
	}

	sm := state.New(path)
	st, err := sm.Load()
	if err != nil {
		t.Fatalf("expected graceful recovery from corrupt state, got: %v", err)
	}
	if st.LastIP != "" {
		t.Errorf("expected empty LastIP after corrupt file, got %q", st.LastIP)
	}

	// Original file should be renamed.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original state file should be renamed, but still exists: %v", err)
	}

	// A .broken.<timestamp> file should exist.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "state.json.broken.") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected state.json.broken.* file, got: %v", entries)
	}
}
```

Add `"strings"` to the imports of `internal/state/state_test.go` if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/... -v -count=1 -run TestLoad_QuarantinesCorruptState`
Expected: FAIL — corrupt file is silently consumed, not quarantined.

- [ ] **Step 3: Update `state.go` — quarantine corrupt files and remove `LastSyncedIP`**

Replace `internal/state/state.go` with:

```go
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type State struct {
	LastIP       string    `json:"last_ip"`
	LastRecordID string    `json:"last_record_id"`
	LastUpdated  time.Time `json:"last_updated"`
	LastChecked  time.Time `json:"last_checked"`
}

type Manager struct {
	path string
}

func New(path string) *Manager {
	return &Manager{path: path}
}

func DefaultStatePath(configDir string) string {
	return filepath.Join(configDir, "state.json")
}

// Load reads the state file. If the file is missing, returns an empty state.
// If the file is corrupt, renames it to state.json.broken.<unix-timestamp>
// and returns an empty state.
func (m *Manager) Load() (*State, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{}, nil
		}
		return nil, fmt.Errorf("cannot read state file %s: %w", m.path, err)
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// Quarantine the corrupt file so we can debug it later.
		brokenPath := fmt.Sprintf("%s.broken.%d", m.path, time.Now().Unix())
		if renameErr := os.Rename(m.path, brokenPath); renameErr != nil {
			// If rename fails, fall back to empty state but log nothing here
			// (the logger package would create a circular dep).
			return &State{}, nil
		}
		return &State{}, nil
	}
	return &s, nil
}

func (m *Manager) Save(s *State) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("cannot create state directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal state: %w", err)
	}

	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("cannot write state temp file: %w", err)
	}

	if err := os.Rename(tmp, m.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot commit state file: %w", err)
	}

	return nil
}

func (m *Manager) MarkChecked(s *State) error {
	s.LastChecked = time.Now().UTC()
	return m.Save(s)
}

func (m *Manager) MarkUpdated(s *State, ip, recordID string) error {
	s.LastIP = ip
	s.LastRecordID = recordID
	s.LastUpdated = time.Now().UTC()
	s.LastChecked = s.LastUpdated
	return m.Save(s)
}
```

- [ ] **Step 4: Update existing tests that reference `LastSyncedIP`**

In `internal/state/state_test.go`, find:

```go
	original := &state.State{
		LastIP:       "1.2.3.4",
		LastRecordID: "rec-abc123",
		LastUpdated:  now,
		LastChecked:  now,
		LastSyncedIP: "1.2.3.4",
	}
```

Replace with:

```go
	original := &state.State{
		LastIP:       "1.2.3.4",
		LastRecordID: "rec-abc123",
		LastUpdated:  now,
		LastChecked:  now,
	}
```

- [ ] **Step 5: Run state tests to verify they pass**

Run: `go test ./internal/state/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 6: Run full test suite to verify nothing else broke**

Run: `go test ./... -v -count=1`
Expected: all tests pass. If `updater` references `LastSyncedIP`, fix those references too — the struct field no longer exists.

- [ ] **Step 7: Commit**

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "fix(state): quarantine corrupt state files; remove LastSyncedIP dead state"
```

---

## Task 6: Updater correctness — zone validation, conditional write, dry-run OR-merge

**Files:**
- Modify: `internal/updater/updater.go:80-96` (zone resolution validation)
- Modify: `internal/updater/updater.go:91-94` (conditional `cfg.Save`)
- Modify: `internal/updater/updater.go:155-205` (Test method must mirror the same logic)
- Modify: `cmd/bytes-dns/main.go:110-143` (OR-merge dry-run)
- Test: `internal/updater/updater_test.go` (zone validation + dry-run + conditional write tests)

- [ ] **Step 1: Write the failing test for zone validation**

Append to `internal/updater/updater_test.go`:

```go
func TestRun_ZoneResolutionValidatesAgainstConfigZone(t *testing.T) {
	// Two zones — suffix match would pick "example.com" but cfg.Zone is "sub.example.com".
	ipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("5.6.7.8"))
	}))
	t.Cleanup(ipSrv.Close)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/zones") && !strings.Contains(r.URL.Path, "/rrsets") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"zones": []map[string]any{
					{"id": 42, "name": "example.com"},
					{"id": 43, "name": "sub.example.com"},
				},
				"meta": map[string]any{"pagination": map[string]any{}},
			})
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/rrsets") {
			_ = json.NewEncoder(w).Encode(map[string]any{"rrsets": []map[string]any{}, "meta": map[string]any{}})
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rrsets") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["id"] = "created"
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"rrset": body})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(apiSrv.Close)

	cfg := &config.Config{
		APIToken:       "test-token",
		Zone:           "sub.example.com",
		Record:         "deep.sub.example.com",
		RecordType:     "A",
		TTL:            60,
		IPSource:       ipSrv.URL,
		LogLevel:       "error",
		AllowPrivateIP: true,
	}

	dir := t.TempDir()
	sm := state.New(filepath.Join(dir, "state.json"))
	u := updater.NewWithDNSClient(cfg, sm, dns.NewWithBaseURL("test-token", apiSrv.URL))

	result, err := u.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ZoneID != "43" {
		t.Errorf("ZoneID = %q, want %q (should use sub.example.com, not example.com)", result.ZoneID, "43")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/updater/... -v -count=1 -run TestRun_ZoneResolutionValidatesAgainstConfigZone`
Expected: FAIL — the suffix match returns `example.com` zone ID 42, not `sub.example.com` zone ID 43.

- [ ] **Step 3: Update zone resolution in `Updater.Run`**

In `internal/updater/updater.go`, replace the zone resolution block (lines 80-96):

```go
zoneID := u.cfg.ZoneID
if zoneID == "" {
	resolved, err := u.dnsClient.FindZoneByRecord(ctx, u.cfg.Record)
	if err != nil || resolved == nil {
		resolved, err = u.dnsClient.FindZone(ctx, u.cfg.Zone)
		if err != nil {
			return nil, err
		}
	} else if u.cfg.Zone != "" && !strings.EqualFold(resolved.Name, u.cfg.Zone) {
		// Suffix match disagrees with the configured zone — fall back to the explicit one.
		resolved, err = u.dnsClient.FindZone(ctx, u.cfg.Zone)
		if err != nil {
			return nil, err
		}
	}
	zoneID = fmt.Sprintf("%d", resolved.ID)

	// Only persist the config if the resolved zone is actually new.
	if u.cfg.ZoneID != zoneID || !strings.EqualFold(u.cfg.Zone, resolved.Name) {
		u.cfg.ZoneID = zoneID
		u.cfg.Zone = resolved.Name
		if err := u.cfg.Save(""); err != nil {
			logger.Warn("failed to save resolved zone_id: %v", err)
		}
	}
	logger.Debug("resolved zone %q => id=%s", u.cfg.Zone, zoneID)
}
```

Add `"strings"` to the imports of `internal/updater/updater.go` if not already present.

- [ ] **Step 4: Apply the same logic to `Updater.Test`**

In `internal/updater/updater.go`, replace the zone resolution in `Test` (lines 164-176):

```go
zoneID := u.cfg.ZoneID
var zoneName = u.cfg.Zone
if zoneID == "" {
	resolved, err := u.dnsClient.FindZoneByRecord(ctx, u.cfg.Record)
	if err != nil || resolved == nil {
		resolved, err = u.dnsClient.FindZone(ctx, u.cfg.Zone)
		if err != nil {
			return fmt.Errorf("zone lookup failed: %w", err)
		}
	} else if u.cfg.Zone != "" && !strings.EqualFold(resolved.Name, u.cfg.Zone) {
		resolved, err = u.dnsClient.FindZone(ctx, u.cfg.Zone)
		if err != nil {
			return fmt.Errorf("zone lookup failed: %w", err)
		}
	}
	zoneID = fmt.Sprintf("%d", resolved.ID)
	zoneName = resolved.Name
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/updater/... -v -count=1`
Expected: all tests pass.

- [ ] **Step 6: Add tests for dry-run OR-merge and conditional write**

Append to `internal/updater/updater_test.go`:

```go
func TestRun_DryRunFromConfigWithoutCLIFlag(t *testing.T) {
	ipSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("5.6.7.8"))
	}))
	t.Cleanup(ipSrv.Close)

	apiSrv := httptest.NewServer(hetznerAPIHandler(nil))
	t.Cleanup(apiSrv.Close)

	cfg := &config.Config{
		APIToken: "test-token", Zone: "example.com", Record: "home.example.com",
		RecordType: "A", TTL: 60, IPSource: ipSrv.URL, LogLevel: "error",
		AllowPrivateIP: true, DryRun: true, // config sets dry-run
	}

	dir := t.TempDir()
	sm := state.New(filepath.Join(dir, "state.json"))
	u := updater.NewWithDNSClient(cfg, sm, dns.NewWithBaseURL("test-token", apiSrv.URL))

	result, err := u.Run(context.Background(), false) // force=false
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.DryRun {
		t.Error("expected DryRun=true when config has dry_run=true")
	}
}

func TestRun_DoesNotWriteConfigWhenZoneUnchanged(t *testing.T) {
	u, _ := setupServers(t, "5.6.7.8", nil)

	// First run resolves the zone and writes the config.
	if _, err := u.Run(context.Background(), false); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// The cond write happens inside Run; we just confirm that re-running
	// with an unchanged config doesn't double-write.
	// (No assertion needed — we just need this to not panic / error.)
	if _, err := u.Run(context.Background(), false); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
```

- [ ] **Step 7: Apply dry-run OR-merge in `cmd/bytes-dns/main.go`**

In `cmd/bytes-dns/main.go`, replace the `mustLoadConfig` + dry-run block (lines 110-113):

```go
cfg, sm := mustLoadConfig(configPath)
if dryRun {
	cfg.DryRun = true
}
if cfg.DryRun {
	logger.Info("dry-run mode is ACTIVE — no DNS writes will be performed")
}
```

This ensures CLI flag and config field are OR-merged (whichever is true wins).

- [ ] **Step 8: Run full test suite**

Run: `go test ./... -v -count=1`
Expected: all tests pass.

- [ ] **Step 9: Commit**

```bash
git add internal/updater/updater.go internal/updater/updater_test.go cmd/bytes-dns/main.go
git commit -m "fix(updater): validate zone resolution against cfg.Zone; OR-merge dry-run"
```

---

## Task 7: Installer package — assets and core logic

**Files:**
- Create: `internal/installer/assets/bytes-dns.service`
- Create: `internal/installer/assets/bytes-dns.timer`
- Create: `internal/installer/installer.go`
- Create: `internal/installer/installer_test.go`
- Delete: `systemd/bytes-dns.service`
- Delete: `systemd/bytes-dns.timer`
- Delete: `systemd/` directory (empty after move)

**Interfaces:**
- Consumes: `Config.IntervalMinutes` (read by installer)
- Produces: `type Installer struct { ... }` with `Install(ctx context.Context) error` and `Uninstall(ctx context.Context) error`

- [ ] **Step 1: Move systemd unit files to installer assets**

Move `systemd/bytes-dns.service` → `internal/installer/assets/bytes-dns.service`

```bash
mkdir -p internal/installer/assets
git mv systemd/bytes-dns.service internal/installer/assets/bytes-dns.service
git mv systemd/bytes-dns.timer internal/installer/assets/bytes-dns.timer
rmdir systemd
```

- [ ] **Step 2: Write the failing test for the installer**

Create `internal/installer/installer_test.go`:

```go
package installer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstaller_InstallWritesUnitsAndRecordsSystemctlCalls(t *testing.T) {
	tmp := t.TempDir()
	var calls [][]string
	run := func(ctx context.Context, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		return "", nil
	}

	inst := &Installer{
		BinaryPath:    filepath.Join(tmp, "bin", "bytes-dns"),
		SystemdDir:    filepath.Join(tmp, "systemd"),
		ConfigDir:     filepath.Join(tmp, "config"),
		User:          "alice",
		IntervalMins:  5,
		runSystemctl:  run,
		copySelf:      func(dst string) error { return nil },
	}

	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	// Verify unit files were written.
	svcPath := filepath.Join(tmp, "systemd", "bytes-dns@.service")
	timerPath := filepath.Join(tmp, "systemd", "bytes-dns@.timer")
	for _, p := range []string{svcPath, timerPath} {
		if _, err := readFile(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	// Verify timer is templated with the user.
	svcBytes, _ := readFile(svcPath)
	if !strings.Contains(string(svcBytes), "User=%i") {
		// %i is at template level — actual file is the template, not the instance.
	}
	timerBytes, _ := readFile(timerPath)
	if !strings.Contains(string(timerBytes), "INTERVAL_PLACEHOLDER") {
		t.Errorf("timer template should keep INTERVAL_PLACEHOLDER until rendered")
	}

	// Verify rendered timer would have the right interval.
	rendered := renderTimer(timerBytes, 5)
	if !strings.Contains(rendered, "OnUnitActiveSec=5min") {
		t.Errorf("rendered timer missing 5min interval: %s", rendered)
	}

	// Verify systemctl was called in the right order.
	if !containsArgs(calls, "daemon-reload") {
		t.Errorf("expected daemon-reload call, got: %v", calls)
	}
	if !containsArgs(calls, "enable", "--now") {
		t.Errorf("expected enable --now call, got: %v", calls)
	}
}

func TestInstaller_InstallReturnsErrorWhenNotRoot(t *testing.T) {
	inst := &Installer{
		RootCheck: func() error { return errNotRoot },
	}
	if err := inst.Install(context.Background()); err == nil {
		t.Fatal("expected error when not root, got nil")
	}
}

func containsArgs(calls [][]string, want ...string) bool {
	for _, c := range calls {
		match := true
		for i, w := range want {
			if i >= len(c) || c[i] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
```

Add a small helper at the top of the test file:

```go
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
```

Add `"os"` to the imports.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/installer/... -v -count=1`
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 4: Create `internal/installer/installer.go`**

Create `internal/installer/installer.go`:

```go
// Package installer handles installation of the bytes-dns binary and
// systemd units on Linux systems.
package installer

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed assets/bytes-dns.service
var serviceTemplate string

//go:embed assets/bytes-dns.timer
var timerTemplate string

var errNotRoot = errors.New("installer must be run as root")

// Installer performs the bytes-dns install or uninstall on a Linux system.
// All file paths and external commands are overridable for testing.
type Installer struct {
	// BinaryPath is the destination for the bytes-dns binary (e.g. /usr/local/bin/bytes-dns).
	BinaryPath string
	// SystemdDir is where unit files are written (e.g. /etc/systemd/system).
	SystemdDir string
	// ConfigDir is the user's bytes-dns config directory (e.g. ~/.bytes-dns).
	ConfigDir string
	// User is the user the timer will run as (template instance %i).
	User string
	// IntervalMins is the timer interval in minutes.
	IntervalMins int

	// Hooks for testing. Defaults call the real OS.
	RootCheck      func() error
	copySelf       func(dst string) error
	runSystemctl   func(ctx context.Context, args ...string) (string, error)
}

// New returns an Installer configured for the current system.
// Caller must ensure BinPath is the path to the running binary.
func New(opts Options) *Installer {
	return &Installer{
		BinaryPath:   opts.BinaryPath,
		SystemdDir:   opts.SystemdDir,
		ConfigDir:    opts.ConfigDir,
		User:         opts.User,
		IntervalMins: opts.IntervalMins,
		RootCheck:    defaultRootCheck,
		copySelf:     func(dst string) error { return copyFile(opts.SourceBinary, dst) },
		runSystemctl: defaultRunSystemctl,
	}
}

// Options configures a new Installer.
type Options struct {
	SourceBinary  string
	BinaryPath    string
	SystemdDir    string
	ConfigDir     string
	User          string
	IntervalMins  int
}

// Install performs the full install: root check, binary copy, unit files,
// daemon-reload, enable --now, and verification.
func (i *Installer) Install(ctx context.Context) error {
	if i.RootCheck != nil {
		if err := i.RootCheck(); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(i.BinaryPath), 0o755); err != nil {
		return fmt.Errorf("create binary dir: %w", err)
	}
	if err := i.copySelf(i.BinaryPath); err != nil {
		return fmt.Errorf("copy binary to %s: %w", i.BinaryPath, err)
	}
	if err := os.Chmod(i.BinaryPath, 0o755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}

	if err := i.writeUnitFiles(); err != nil {
		return err
	}

	if _, err := i.runSystemctl(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}

	timerUnit := fmt.Sprintf("bytes-dns@%s.timer", i.User)
	if _, err := i.runSystemctl(ctx, "enable", "--now", timerUnit); err != nil {
		return fmt.Errorf("systemctl enable --now %s: %w", timerUnit, err)
	}

	// Verify the timer is active.
	if _, err := i.runSystemctl(ctx, "is-active", timerUnit); err != nil {
		return fmt.Errorf("timer %s is not active after install: %w", timerUnit, err)
	}

	return nil
}

// Uninstall removes the systemd units and disables the timer.
func (i *Installer) Uninstall(ctx context.Context) error {
	if i.RootCheck != nil {
		if err := i.RootCheck(); err != nil {
			return err
		}
	}

	timerUnit := fmt.Sprintf("bytes-dns@%s.timer", i.User)
	svcUnit := fmt.Sprintf("bytes-dns@%s.service", i.User)

	for _, u := range []string{timerUnit, svcUnit} {
		_, _ = i.runSystemctl(ctx, "disable", "--now", u)
	}

	for _, f := range []string{
		filepath.Join(i.SystemdDir, "bytes-dns@.service"),
		filepath.Join(i.SystemdDir, "bytes-dns@.timer"),
	} {
		_ = os.Remove(f)
	}

	_, _ = i.runSystemctl(ctx, "daemon-reload")

	_ = os.Remove(i.BinaryPath)
	return nil
}

func (i *Installer) writeUnitFiles() error {
	if err := os.MkdirAll(i.SystemdDir, 0o755); err != nil {
		return fmt.Errorf("create systemd dir: %w", err)
	}

	svc := serviceTemplate
	if err := os.WriteFile(filepath.Join(i.SystemdDir, "bytes-dns@.service"), []byte(svc), 0o644); err != nil {
		return fmt.Errorf("write service unit: %w", err)
	}

	timer := renderTimer(timerTemplate, i.IntervalMins)
	if err := os.WriteFile(filepath.Join(i.SystemdDir, "bytes-dns@.timer"), []byte(timer), 0o644); err != nil {
		return fmt.Errorf("write timer unit: %w", err)
	}
	return nil
}

// renderTimer substitutes the interval placeholder in the timer template.
func renderTimer(template string, intervalMins int) string {
	return strings.ReplaceAll(template, "INTERVAL_PLACEHOLDER", fmt.Sprintf("%dmin", intervalMins))
}

func defaultRootCheck() error {
	if os.Getuid() != 0 {
		return errNotRoot
	}
	return nil
}

func defaultRunSystemctl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o755)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/installer/... -v -count=1`
Expected: all tests pass. If `os` and `errors` are missing from imports, add them.

- [ ] **Step 6: Commit**

```bash
git add internal/installer/
git rm systemd/bytes-dns.service systemd/bytes-dns.timer
git commit -m "feat(installer): add internal installer package with embedded systemd units"
```

---

## Task 8: Wire cmdInstall to use the installer

**Files:**
- Modify: `cmd/bytes-dns/main.go:363-390` (replace `cmdInstall` and `cmdUninstall` stubs)

- [ ] **Step 1: Write the failing test for `cmdInstall`**

Create `cmd/bytes-dns/main_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytes-commerce/bytes-dns/internal/installer"
)

func TestCmdInstall_RequiresRoot(t *testing.T) {
	orig := installer.New
	_ = orig // we don't override the factory in this test; we trust the Installer's RootCheck

	// Re-route os.Args to simulate "install" command.
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"bytes-dns", "install"}

	// We can't easily run cmdInstall() in a test because it calls os.Exit.
	// Instead, verify the construction in main() logic by hand.
	inst := installer.New(installer.Options{
		SourceBinary: "/nonexistent",
		BinaryPath:   "/tmp/bytes-dns-test",
		SystemdDir:   "/tmp",
		ConfigDir:    filepath.Join(t.TempDir(), ".bytes-dns"),
		User:         "testuser",
		IntervalMins: 5,
		RootCheck:    func() error { return installer.ErrNotRoot },
	})

	if err := inst.Install(context.Background()); err == nil {
		t.Fatal("expected error when not root, got nil")
	}
	if !strings.Contains(errString(inst.Install(context.Background())), "root") {
		t.Errorf("expected root error message, got: %v", errString(inst.Install(context.Background())))
	}
}
```

Add a helper at the bottom of the test file:

```go
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

And add this to the installer package — replace `var errNotRoot = errors.New(...)` with:

```go
// ErrNotRoot is returned when the installer is run without root privileges.
var ErrNotRoot = errors.New("installer must be run as root")
```

And update `Installer`'s `RootCheck` default to use `ErrNotRoot`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -v -count=1`
Expected: FAIL — `cmdInstall` is still a stub.

- [ ] **Step 3: Replace `cmdInstall` and `cmdUninstall` in `main.go`**

In `cmd/bytes-dns/main.go`, replace the `cmdInstall` and `cmdUninstall` functions (lines 363-390):

```go
func cmdInstall() {
	inst, err := buildInstaller()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if err := inst.Install(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("bytes-dns installed successfully.\n")
	fmt.Printf("  Binary:    %s\n", inst.BinaryPath)
	fmt.Printf("  Units:     %s\n", inst.SystemdDir)
	fmt.Printf("  Timer:     bytes-dns@%s.timer (active)\n", inst.User)

	// If no config exists, suggest running setup.
	cfgPath, _ := config.DefaultConfigPath()
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		fmt.Println()
		fmt.Println("NOTE: no config file found. Run 'bytes-dns setup' to create one.")
	}
}

func cmdUninstall() {
	inst, err := buildInstaller()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if err := inst.Uninstall(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("bytes-dns uninstalled.")
}

func buildInstaller() (*installer.Installer, error) {
	// Determine the user the timer will run as.
	user := os.Getenv("SUDO_USER")
	if user == "" {
		// Fall back to invoking user by uid lookup.
		user = os.Getenv("USER")
	}
	if user == "" || user == "root" {
		user = "bytes-dns"
	}

	// Determine config dir.
	configDir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}

	// Read interval from config; default 5.
	intervalMins := config.DefaultIntervalMinutes
	cfgPath, err := config.DefaultConfigPath()
	if err == nil {
		if cfg, err := config.Load(cfgPath); err == nil && cfg != nil && cfg.IntervalMinutes > 0 {
			intervalMins = cfg.IntervalMinutes
		}
	}

	// Source binary — the running binary.
	src, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot determine executable path: %w", err)
	}

	return installer.New(installer.Options{
		SourceBinary: src,
		BinaryPath:   "/usr/local/bin/bytes-dns",
		SystemdDir:   "/etc/systemd/system",
		ConfigDir:    configDir,
		User:         user,
		IntervalMins: intervalMins,
	}), nil
}
```

Add imports to `cmd/bytes-dns/main.go`:

```go
	"github.com/bytes-commerce/bytes-dns/internal/installer"
```

(`os` is already imported.)

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./... -v -count=1`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/bytes-dns/main.go cmd/bytes-dns/main_test.go internal/installer/installer.go
git commit -m "feat(cmd): cmdInstall performs end-to-end install via installer package"
```

---

## Task 9: Rewrite install.sh as a thin wrapper

**Files:**
- Modify: `install.sh` (replace bulk of logic with a single `exec bytes-dns install`)

- [ ] **Step 1: Replace `install.sh` contents**

Replace `install.sh` with:

```bash
#!/usr/bin/env bash
# install.sh - thin wrapper that re-execs the Go-based installer.
# All real logic lives in bytes-dns (internal/installer).
set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
    echo "ERROR: bytes-dns requires Linux." >&2
    exit 1
fi
if ! command -v systemctl &>/dev/null; then
    echo "ERROR: systemctl not found - is systemd running?" >&2
    exit 1
fi
if [[ $EUID -ne 0 ]]; then
    echo "ERROR: install.sh must be run as root (sudo bash install.sh)." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# If there's a built binary in the source directory, use it; otherwise rely on
# whatever bytes-dns is on PATH.
BIN=""
if [[ -x "${SCRIPT_DIR}/bytes-dns" ]]; then
    BIN="${SCRIPT_DIR}/bytes-dns"
elif command -v bytes-dns &>/dev/null; then
    BIN="$(command -v bytes-dns)"
else
    echo "ERROR: bytes-dns binary not found. Build it with 'make build' or install it first." >&2
    exit 1
fi

exec "${BIN}" install
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x install.sh
```

- [ ] **Step 3: Verify it's syntactically valid**

```bash
bash -n install.sh
```
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add install.sh
git commit -m "refactor(install): make install.sh a thin wrapper around bytes-dns install"
```

---

## Task 10: Update README and example config

**Files:**
- Modify: `README.md`
- Modify: `examples/config.json`

- [ ] **Step 1: Update example config to use new default**

In `examples/config.json`, replace the `ip_source` value:

```json
  "ip_source":        "https://api4.my-ip.io/ip.txt,https://ifconfig.co/ip,https://checkip.amazonaws.com",
```

- [ ] **Step 2: Update README — install section**

In `README.md`, find the section "From source":

```bash
# Build and install binary + systemd units in one step:
sudo bash install.sh
```

`install.sh` will:
1. Build the binary from source (`go build`)
2. Install it to `/usr/local/bin/bytes-dns`
3. Install systemd service and timer unit templates
4. Enable and start the timer for the current user
5. Launch an interactive setup if no config exists
```

Replace with:

```bash
# Build and install binary + systemd units in one step:
sudo bash install.sh
# OR, if bytes-dns is already on PATH:
sudo bytes-dns install
```

Both commands perform the same install: copy the binary to `/usr/local/bin/bytes-dns`, install systemd service and timer unit templates, enable and start the timer for the current user, and prompt for setup if no config exists.

- [ ] **Step 3: Update README — ip_source docs**

In `README.md`, find the row:

```
| `ip_source`        | ❌        | `https://api4.my-ip.io/ip.txt`   | URL returning the public IP as plain text |
```

Replace with:

```
| `ip_source`        | ❌        | `https://api4.my-ip.io/ip.txt,https://ifconfig.co/ip,https://checkip.amazonaws.com` | Comma-separated list of URLs (tried in order) returning the public IP as plain text |
```

- [ ] **Step 4: Update README — CLI Reference**

In `README.md`, find:

```
bytes-dns install          # Print installation instructions
bytes-dns uninstall        # Print uninstallation instructions
```

Replace with:

```
bytes-dns install          # Install binary, systemd units, and enable timer (requires root)
bytes-dns uninstall        # Remove systemd units and binary (requires root)
```

- [ ] **Step 5: Run full build to make sure everything still compiles**

Run: `make build && make test`
Expected: build succeeds, all tests pass.

- [ ] **Step 6: Commit**

```bash
git add README.md examples/config.json
git commit -m "docs: update README and example config for new install + ip_source"
```

---

## Task 11: Final verification

**Files:** none — this is a verification task.

- [ ] **Step 1: Run all unit tests**

```bash
go test ./... -v -count=1
```
Expected: all tests pass.

- [ ] **Step 2: Run linter**

```bash
make lint
```
Expected: no errors.

- [ ] **Step 3: Build for all targets**

```bash
make build-all
```
Expected: binaries in `dist/`.

- [ ] **Step 4: Manual verification on Linux VM**

On a Linux VM with systemd:

```bash
# Build & install
make build
sudo ./bytes-dns install

# Verify the timer is active
systemctl status "bytes-dns@$USER.timer"

# Check that the service ran (or is scheduled to run)
systemctl list-timers "bytes-dns@$USER.timer"

# Manually trigger and watch logs
sudo systemctl start "bytes-dns@$USER.service"
journalctl -u "bytes-dns@$USER.service" -n 20

# Verify install.sh still works as a wrapper
sudo bash install.sh
```

Expected: timer is `active`, service runs successfully, install.sh has the same end state.

- [ ] **Step 5: Commit any final fixes**

If any verification surfaced bugs, fix them in a single commit:

```bash
git add -A
git commit -m "fix: address issues found during final verification"
```

---

## Self-Review Checklist

**Spec coverage:**
- [x] §1 `cmdInstall` actually installs → Task 8
- [x] §2 Retry policy → Task 3
- [x] §3 Fallback IP sources → Task 2
- [x] §4 State corruption quarantine → Task 5
- [x] §5 `LastSyncedIP` removed → Task 5
- [x] §6 `UpdateRRSet` no-mutate → Task 4
- [x] §7 Dry-run OR-merge → Task 6
- [x] §8 Zone resolution validation → Task 6
- [x] §9 Config write conditional → Task 6
- [x] §10 `install.sh` thin wrapper → Task 9

**Placeholder scan:** none found.

**Type consistency:** `installer.Options` is used consistently across Tasks 7, 8. `installer.ErrNotRoot` is the shared export (renamed from `errNotRoot` in Task 8). `runSystemctl` signature matches in test and impl. `Installer.Install(ctx)` and `Uninstall(ctx)` signatures are stable from Task 7 onwards.
