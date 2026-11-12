package garmin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "test-api-key-123"

// fakeJWT returns a syntactically valid unsigned JWT that expires at exp.
func fakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(fmt.Appendf(nil, `{"exp":%d}`, exp.Unix())) + ".c2ln"
}

type fake struct {
	srv         *httptest.Server
	tokenCalls  atomic.Int32
	dataCalls   atomic.Int32
	tokenExp    time.Duration
	handler     func(w http.ResponseWriter, r *http.Request)
	lastTokenTo atomic.Value
}

func newFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *fake {
	t.Helper()
	f := &fake{tokenExp: 24 * time.Hour, handler: h}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			f.tokenCalls.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "ApiKey "+testKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if strings.Contains(r.Header.Get("Accept"), "application/json") && !strings.Contains(r.Header.Get("Accept"), "*/*") {
				w.WriteHeader(http.StatusNotAcceptable) // mirrors the real API
				return
			}
			tok := fakeJWT(time.Now().Add(f.tokenExp))
			f.lastTokenTo.Store(tok)
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, tok)
			return
		}
		f.dataCalls.Add(1)
		tok, _ := f.lastTokenTo.Load().(string)
		if r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.handler(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newTestClient(t *testing.T, f *fake) *Client {
	t.Helper()
	c, err := New(testKey, WithBaseURL(f.srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetExchangesKeyAndReusesToken(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"ok":true}`) })
	c := newTestClient(t, f)
	for range 3 {
		body, err := c.Get(context.Background(), "/x", nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"ok":true}` {
			t.Fatalf("body = %s", body)
		}
	}
	if n := f.tokenCalls.Load(); n != 1 {
		t.Fatalf("token calls = %d, want 1", n)
	}
}

func TestTokenRenewedNearExpiry(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{}`) })
	f.tokenExp = 2 * time.Minute // inside refreshMargin
	c := newTestClient(t, f)
	for range 2 {
		if _, err := c.Get(context.Background(), "/x", nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.tokenCalls.Load(); n != 2 {
		t.Fatalf("token calls = %d, want 2", n)
	}
}

func TestReauthOn401(t *testing.T) {
	var calls atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	c := newTestClient(t, f)
	if _, err := c.Get(context.Background(), "/x", nil); err != nil {
		t.Fatal(err)
	}
	if n := f.tokenCalls.Load(); n != 2 {
		t.Fatalf("token calls = %d, want 2", n)
	}
}

func TestPersistent401IsError(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	c := newTestClient(t, f)
	_, err := c.Get(context.Background(), "/x", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want APIError 401", err)
	}
}

func TestRetryOn429(t *testing.T) {
	var calls atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	c := newTestClient(t, f)
	if _, err := c.Get(context.Background(), "/x", nil); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("calls = %d, want 3", n)
	}
}

func TestNoContentIsErrNoData(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	c := newTestClient(t, f)
	if _, err := c.Get(context.Background(), "/x", nil); !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
}

func TestBadRequestNotRetried(t *testing.T) {
	var calls atomic.Int32
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{}`)
	})
	c := newTestClient(t, f)
	_, err := c.Get(context.Background(), "/x", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("400 was retried")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	defer other.Close()
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	})
	c := newTestClient(t, f)
	if _, err := c.Get(context.Background(), "/x", nil); err == nil {
		t.Fatal("want error for redirect response")
	}
	if leaked.Load() {
		t.Fatal("credentials sent to redirect target")
	}
}

func TestBadKeyErrorDoesNotLeakKey(t *testing.T) {
	f := newFake(t, nil)
	c, err := New("wrong-key", WithBaseURL(f.srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(context.Background(), "/x", nil)
	if err == nil || strings.Contains(err.Error(), "wrong-key") || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRejectsBadKeys(t *testing.T) {
	for _, k := range []string{"", "  ", "a b", "a\tb", "a\nb"} {
		if _, err := New(k); err == nil {
			t.Errorf("New(%q) succeeded", k)
		}
	}
}

func TestJWTExpiry(t *testing.T) {
	exp := time.Unix(1_800_000_000, 0)
	got, err := jwtExpiry(fakeJWT(exp))
	if err != nil || !got.Equal(exp) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"", "a.b", "a.b.c.d", "a.!!.c", "<html>.x.y"} {
		if _, err := jwtExpiry(bad); err == nil {
			t.Errorf("jwtExpiry(%q) succeeded", bad)
		}
	}
}

func TestTokenRefreshIsSingleFlight(t *testing.T) {
	release := make(chan struct{})
	var tokenCalls atomic.Int32
	tok := fakeJWT(time.Now().Add(time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			tokenCalls.Add(1)
			<-release
			fmt.Fprint(w, tok)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	defer close(release)
	c, err := New(testKey, WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	// A caller with a short deadline must not wait for the slow refresh.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Get(ctx, "/x", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("caller blocked for %v", d)
	}

	// Concurrent callers share the in-flight refresh.
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Go(func() {
			_, err := c.Get(context.Background(), "/x", nil)
			errs <- err
		})
	}
	time.Sleep(50 * time.Millisecond)
	release <- struct{}{}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := tokenCalls.Load(); n != 1 {
		t.Fatalf("token calls = %d, want 1", n)
	}
}

func TestBackoffRetryAfter(t *testing.T) {
	if d := backoff(0, "2"); d != 2*time.Second {
		t.Errorf("seconds form: %v", d)
	}
	if d := backoff(0, "3600"); d != 30*time.Second {
		t.Errorf("cap: %v", d)
	}
	if d := backoff(0, time.Now().Add(5*time.Second).UTC().Format(http.TimeFormat)); d < 3*time.Second || d > 6*time.Second {
		t.Errorf("date form: %v", d)
	}
	if d := backoff(1, ""); d < time.Second || d >= 1500*time.Millisecond {
		t.Errorf("exponential: %v", d)
	}
}
