package watch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/adapters/clock"
	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

func TestURLFetcherObservesStatusBodyLatencyAndTLSCertificate(t *testing.T) {
	clk := clock.NewFake(watchNow)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		clk.Advance(125 * time.Millisecond)
		_, _ = w.Write([]byte("zorgscope is healthy"))
	}))
	defer srv.Close()
	f, err := NewURLFetcher(srv.Client(), URLCheck{Name: "zorgscope", URL: srv.URL, ExpectStatus: 200, ExpectBodyContains: "healthy"}, clk)
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind() != ports.KindWatchURL || f.ID() != "watch:url:zorgscope" {
		t.Fatalf("id/kind = %s/%s", f.ID(), f.Kind())
	}
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != domain.KindHealthCheck || items[0].URL != srv.URL {
		t.Fatalf("items = %+v", items)
	}
	p, err := domain.DecodePayload[domain.HealthCheckPayload](items[0])
	if err != nil {
		t.Fatal(err)
	}
	if !p.OK || p.StatusCode != 200 || p.LatencyMs != 125 || p.ConsecutiveFailures != 0 || p.CertExpires == nil ||
		p.CertIssuer == "" || p.HostnameValid == nil || !*p.HostnameValid || !p.CheckedAt.Equal(watchNow.Add(125*time.Millisecond)) ||
		!p.LastOK.Equal(watchNow.Add(125*time.Millisecond)) {
		t.Fatalf("payload = %+v", p)
	}
}

func TestURLFetcherTracksConsecutiveFailuresAndLastOK(t *testing.T) {
	clk := clock.NewFake(watchNow)
	var mu sync.Mutex
	healthy := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		ok := healthy
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = w.Write([]byte("ready"))
	}))
	defer srv.Close()
	f, err := NewURLFetcher(srv.Client(), URLCheck{Name: "app", URL: srv.URL, ExpectBodyContains: "ready"}, clk)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := f.Fetch(context.Background())
	good, _ := domain.DecodePayload[domain.HealthCheckPayload](first[0])
	mu.Lock()
	healthy = false
	mu.Unlock()
	clk.Advance(time.Minute)
	second, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	third, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p1, _ := domain.DecodePayload[domain.HealthCheckPayload](second[0])
	p2, _ := domain.DecodePayload[domain.HealthCheckPayload](third[0])
	if p1.OK || p1.ConsecutiveFailures != 1 || p2.OK || p2.ConsecutiveFailures != 2 || !p2.LastOK.Equal(good.LastOK) {
		t.Fatalf("failure payloads = %+v / %+v", p1, p2)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network unavailable")
}

type switchableTransport struct {
	mu   sync.Mutex
	base http.RoundTripper
	fail bool
}

func (t *switchableTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	fail := t.fail
	t.mu.Unlock()
	if fail {
		return nil, errors.New("network unavailable")
	}
	return t.base.RoundTrip(request)
}

func (t *switchableTransport) setFail(fail bool) {
	t.mu.Lock()
	t.fail = fail
	t.mu.Unlock()
}

func TestURLFetcherPreservesCertificateAcrossTransportFailure(t *testing.T) {
	clk := clock.NewFake(watchNow)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("healthy"))
	}))
	defer srv.Close()
	transport := &switchableTransport{base: srv.Client().Transport}
	f, err := NewURLFetcher(&http.Client{Transport: transport}, URLCheck{Name: "app", URL: srv.URL}, clk)
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := domain.DecodePayload[domain.HealthCheckPayload](first[0])
	transport.setFail(true)
	clk.Advance(time.Minute)
	failed, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := domain.DecodePayload[domain.HealthCheckPayload](failed[0])
	if before.CertExpires == nil || after.CertExpires == nil || !after.CertExpires.Equal(*before.CertExpires) ||
		after.CertIssuer != before.CertIssuer || after.HostnameValid == nil || !*after.HostnameValid {
		t.Fatalf("certificate not preserved: before=%+v after=%+v", before, after)
	}
	if !after.CheckedAt.Equal(watchNow.Add(time.Minute)) || !after.LastOK.Equal(before.LastOK) {
		t.Fatalf("check timestamps = %+v", after)
	}
}

func TestURLFetcherUpdatedAtTracksMeaningfulStateOnly(t *testing.T) {
	clk := clock.NewFake(watchNow)
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	f, err := NewURLFetcher(srv.Client(), URLCheck{Name: "app", URL: srv.URL}, clk)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := f.Fetch(context.Background())
	clk.Advance(time.Minute)
	second, _ := f.Fetch(context.Background())
	if !second[0].UpdatedAt.Equal(first[0].UpdatedAt) || !second[0].CreatedAt.After(first[0].CreatedAt) {
		t.Fatalf("identical healthy poll changed state timestamp: first=%+v second=%+v", first[0], second[0])
	}
	secondPayload, _ := domain.DecodePayload[domain.HealthCheckPayload](second[0])
	if !secondPayload.CheckedAt.Equal(watchNow.Add(time.Minute)) {
		t.Fatalf("checked_at did not advance: %+v", secondPayload)
	}

	status = http.StatusServiceUnavailable
	clk.Advance(time.Minute)
	degraded, _ := f.Fetch(context.Background())
	clk.Advance(time.Minute)
	down, _ := f.Fetch(context.Background())
	clk.Advance(time.Minute)
	stillDown, _ := f.Fetch(context.Background())
	if !degraded[0].UpdatedAt.After(second[0].UpdatedAt) || !down[0].UpdatedAt.After(degraded[0].UpdatedAt) ||
		!stillDown[0].UpdatedAt.Equal(down[0].UpdatedAt) {
		t.Fatalf("failure threshold timestamps: degraded=%s down=%s still=%s", degraded[0].UpdatedAt, down[0].UpdatedAt, stillDown[0].UpdatedAt)
	}
	status = http.StatusInternalServerError
	clk.Advance(time.Minute)
	changedStatus, _ := f.Fetch(context.Background())
	if !changedStatus[0].UpdatedAt.After(stillDown[0].UpdatedAt) {
		t.Fatalf("status change did not revoke state timestamp: %s", changedStatus[0].UpdatedAt)
	}
}

func TestURLFetcherUpdatedAtChangesForCertificateIdentityOrExpiry(t *testing.T) {
	f := &URLFetcher{check: URLCheck{Name: "app", URL: "https://app.example"}, id: "watch:url:app"}
	t1 := watchNow
	valid := true
	cert1 := &x509.Certificate{Raw: []byte("certificate-one"), SerialNumber: big.NewInt(1), NotAfter: t1.Add(30 * 24 * time.Hour), Issuer: pkix.Name{CommonName: "issuer"}}
	cert2 := &x509.Certificate{Raw: []byte("certificate-two"), SerialNumber: big.NewInt(2), NotAfter: cert1.NotAfter, Issuer: cert1.Issuer}
	details1 := detailsForCertificate(cert1, &valid)
	details2 := detailsForCertificate(cert2, &valid)
	first := f.result(t1, 200, 10, true, &details1)
	identical := f.result(t1.Add(time.Minute), 200, 99, true, &details1)
	changedIdentity := f.result(t1.Add(time.Minute), 200, 10, true, &details2)
	cert2.NotAfter = cert2.NotAfter.Add(24 * time.Hour)
	details3 := detailsForCertificate(cert2, &valid)
	changedExpiry := f.result(t1.Add(time.Minute), 200, 10, true, &details3)
	if !identical.UpdatedAt.Equal(first.UpdatedAt) || !changedIdentity.UpdatedAt.After(identical.UpdatedAt) || !changedExpiry.UpdatedAt.After(changedIdentity.UpdatedAt) {
		t.Fatalf("certificate timestamps: first=%s same=%s identity=%s expiry=%s", first.UpdatedAt, identical.UpdatedAt, changedIdentity.UpdatedAt, changedExpiry.UpdatedAt)
	}
}

func TestCertificateMetadataExtractedFromVerificationError(t *testing.T) {
	expires := watchNow.Add(30 * 24 * time.Hour)
	certificate := &x509.Certificate{
		Raw: []byte("unverified-leaf"), NotAfter: expires, DNSNames: []string{"good.example"},
		Issuer: pkix.Name{CommonName: "Test Issuer"},
	}
	verificationErr := &tls.CertificateVerificationError{
		UnverifiedCertificates: []*x509.Certificate{certificate},
		Err:                    x509.HostnameError{Certificate: certificate, Host: "bad.example"},
	}
	err := &url.Error{Op: "Get", URL: "https://bad.example/health", Err: verificationErr}
	got, hostnameValid := certificateFromError(err, requestHostname(err, "fallback.example"))
	if got != certificate || hostnameValid == nil || *hostnameValid || got.Issuer.String() != "CN=Test Issuer" {
		t.Fatalf("certificate metadata = cert:%p valid:%v issuer:%q", got, hostnameValid, got.Issuer.String())
	}
}

func TestURLFetcherMapsTransportFailureToHealthObservation(t *testing.T) {
	hc := &http.Client{Transport: failingTransport{}}
	f, err := NewURLFetcher(hc, URLCheck{Name: "offline", URL: "https://offline.invalid"}, clock.NewFake(watchNow))
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := domain.DecodePayload[domain.HealthCheckPayload](items[0])
	if p.OK || p.StatusCode != 0 || p.ConsecutiveFailures != 1 {
		t.Fatalf("payload = %+v", p)
	}
}

func TestURLFetcherReturnsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, _ := NewURLFetcher(nil, URLCheck{Name: "app", URL: "https://example.invalid"}, clock.NewFake(watchNow))
	if _, err := f.Fetch(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestNewURLFetcherValidatesConfig(t *testing.T) {
	cases := []URLCheck{
		{Name: "", URL: "https://example.com"},
		{Name: "bad|name", URL: "https://example.com"},
		{Name: "app", URL: "relative"},
		{Name: "app", URL: "ftp://example.com"},
		{Name: "app", URL: "https://example.com", ExpectStatus: 999},
	}
	for _, check := range cases {
		if _, err := NewURLFetcher(nil, check, clock.NewFake(watchNow)); !errors.Is(err, ports.ErrPermanent) {
			t.Fatalf("check %+v: %v", check, err)
		}
	}
}
