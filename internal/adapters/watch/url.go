package watch

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

const maxHealthBody = 4 << 20

// URLCheck configures one HTTP health observation.
type URLCheck struct {
	Name               string
	URL                string
	ExpectStatus       int
	ExpectBodyContains string
}

// URLFetcher observes one URL. Health failures are returned as KindHealthCheck items rather than
// scheduler errors: they are the data this source exists to collect.
type URLFetcher struct {
	http  *http.Client
	check URLCheck
	clock ports.Clock
	id    string

	mu                  sync.Mutex
	consecutiveFailures int
	lastOK              time.Time
	certificate         certificateDetails
	state               endpointState
	stateKnown          bool
	stateUpdatedAt      time.Time
}

type certificateDetails struct {
	known         bool
	expires       time.Time
	issuer        string
	hostnameValid *bool
	identity      [sha256.Size]byte
}

type endpointState struct {
	availability  uint8 // 0 healthy, 1 first failure, 2 down (two or more failures)
	statusCode    int
	certKnown     bool
	certExpires   int64
	certIssuer    string
	hostnameKnown bool
	hostnameValid bool
	certIdentity  [sha256.Size]byte
}

// NewURLFetcher validates check and creates its independent source fetcher.
func NewURLFetcher(hc *http.Client, check URLCheck, clock ports.Clock) (*URLFetcher, error) {
	if clock == nil {
		return nil, fmt.Errorf("%w: URL watch clock is required", ports.ErrPermanent)
	}
	if strings.TrimSpace(check.Name) == "" || strings.Contains(check.Name, "|") {
		return nil, fmt.Errorf("%w: URL check name is empty or contains the reserved '|' character", ports.ErrPermanent)
	}
	parsed, err := url.Parse(check.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("%w: URL check requires an absolute http(s) URL", ports.ErrPermanent)
	}
	if check.ExpectStatus == 0 {
		check.ExpectStatus = http.StatusOK
	}
	if check.ExpectStatus < 100 || check.ExpectStatus > 599 {
		return nil, fmt.Errorf("%w: expected HTTP status must be 100..599", ports.ErrPermanent)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	} else if hc.Timeout == 0 || hc.Timeout > 10*time.Second {
		clone := *hc
		clone.Timeout = 10 * time.Second
		hc = &clone
	}
	return &URLFetcher{http: hc, check: check, clock: clock, id: "watch:url:" + url.QueryEscape(check.Name)}, nil
}

// ID implements ports.SourceFetcher.
func (f *URLFetcher) ID() string { return f.id }

// Kind implements ports.SourceFetcher.
func (*URLFetcher) Kind() string { return ports.KindWatchURL }

// Fetch implements ports.SourceFetcher.
func (f *URLFetcher) Fetch(ctx context.Context) ([]domain.Item, error) {
	started := f.clock.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.check.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build health-check request: %v", ports.ErrPermanent, err)
	}
	req.Header.Set("Accept", "text/plain, text/html, application/json;q=0.9, */*;q=0.1")
	resp, requestErr := f.http.Do(req)
	finished := f.clock.Now()
	latency := finished.Sub(started).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	if requestErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var observed *certificateDetails
		if certificate, hostnameValid := certificateFromError(requestErr, requestHostname(requestErr, req.URL.Hostname())); certificate != nil {
			details := detailsForCertificate(certificate, hostnameValid)
			observed = &details
		}
		return []domain.Item{f.result(finished, 0, latency, false, observed)}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	observed := certificateFromResponse(resp)

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHealthBody+1))
	if readErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ok := readErr == nil && len(body) <= maxHealthBody && resp.StatusCode == f.check.ExpectStatus
	if ok && f.check.ExpectBodyContains != "" {
		ok = strings.Contains(string(body), f.check.ExpectBodyContains)
	}
	return []domain.Item{f.result(finished, resp.StatusCode, latency, ok, &observed)}, nil
}

func (f *URLFetcher) result(now time.Time, status int, latency int64, ok bool, observed *certificateDetails) domain.Item {
	f.mu.Lock()
	if ok {
		f.consecutiveFailures = 0
		f.lastOK = now
	} else {
		f.consecutiveFailures++
	}
	if observed != nil {
		f.certificate = *observed
	}
	signature := f.endpointState(status, ok)
	if !f.stateKnown || signature != f.state {
		updatedAt := now.UTC()
		// Dismissals and SQLite compare timestamps at Unix-second precision. Preserve an actual state
		// transition even when two observations happen within the same clock second.
		if f.stateKnown && updatedAt.Unix() <= f.stateUpdatedAt.Unix() {
			updatedAt = f.stateUpdatedAt.Add(time.Second)
		}
		f.state, f.stateKnown, f.stateUpdatedAt = signature, true, updatedAt
	}
	failures, lastOK := f.consecutiveFailures, f.lastOK
	certificate, updatedAt := f.certificate, f.stateUpdatedAt
	f.mu.Unlock()

	var certExpires *time.Time
	if certificate.known {
		expires := certificate.expires
		certExpires = &expires
	}
	hostnameValid := cloneBool(certificate.hostnameValid)
	return domain.Item{
		ID:        domain.ItemID{SourceID: f.ID(), ExternalID: "status"},
		Kind:      domain.KindHealthCheck,
		Title:     f.check.Name,
		URL:       f.check.URL,
		CreatedAt: now,
		UpdatedAt: updatedAt,
		Payload: domain.MustPayload(domain.HealthCheckPayload{
			StatusCode: status, LatencyMs: latency, OK: ok, ConsecutiveFailures: failures,
			CertExpires: certExpires, CertIssuer: certificate.issuer, HostnameValid: hostnameValid,
			CheckedAt: now, LastOK: lastOK,
		}),
	}
}

func (f *URLFetcher) endpointState(status int, ok bool) endpointState {
	availability := uint8(0)
	if !ok {
		availability = 1
		if f.consecutiveFailures >= 2 {
			availability = 2
		}
	}
	state := endpointState{
		availability: availability, statusCode: status, certKnown: f.certificate.known,
		certIssuer: f.certificate.issuer, certIdentity: f.certificate.identity,
	}
	if f.certificate.known {
		state.certExpires = f.certificate.expires.UnixNano()
	}
	if f.certificate.hostnameValid != nil {
		state.hostnameKnown, state.hostnameValid = true, *f.certificate.hostnameValid
	}
	return state
}

// certificateFromResponse returns an observed certificate state. A successful HTTPS response has
// passed the client's normal verification, so hostname validity is true. A successful plain HTTP
// response deliberately returns an observed empty state, clearing any certificate from a redirect
// or prior configuration rather than treating its absence as a transient failure.
func certificateFromResponse(resp *http.Response) certificateDetails {
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return certificateDetails{}
	}
	valid := true
	return detailsForCertificate(resp.TLS.PeerCertificates[0], &valid)
}

func detailsForCertificate(certificate *x509.Certificate, hostnameValid *bool) certificateDetails {
	if certificate == nil {
		return certificateDetails{}
	}
	return certificateDetails{
		known: true, expires: certificate.NotAfter.UTC(), issuer: certificate.Issuer.String(),
		hostnameValid: cloneBool(hostnameValid), identity: sha256.Sum256(certificate.Raw),
	}
}

// certificateFromError recovers the unverified leaf exposed by Go's TLS/x509 verification errors.
// Verification remains enabled: this only observes the certificate attached to the failed request.
func certificateFromError(err error, hostname string) (*x509.Certificate, *bool) {
	var certificate *x509.Certificate
	var verificationErr *tls.CertificateVerificationError
	if errors.As(err, &verificationErr) && len(verificationErr.UnverifiedCertificates) > 0 {
		certificate = verificationErr.UnverifiedCertificates[0]
	}
	if certificate == nil {
		var hostnameErr x509.HostnameError
		if errors.As(err, &hostnameErr) {
			certificate = hostnameErr.Certificate
		}
	}
	if certificate == nil {
		var invalidErr x509.CertificateInvalidError
		if errors.As(err, &invalidErr) {
			certificate = invalidErr.Cert
		}
	}
	if certificate == nil {
		var authorityErr x509.UnknownAuthorityError
		if errors.As(err, &authorityErr) {
			certificate = authorityErr.Cert
		}
	}
	if certificate == nil || hostname == "" {
		return certificate, nil
	}
	valid := certificate.VerifyHostname(hostname) == nil
	return certificate, &valid
}

func requestHostname(err error, fallback string) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil && parsed.Hostname() != "" {
			return parsed.Hostname()
		}
	}
	return fallback
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
