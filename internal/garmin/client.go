// Package garmin is a read-only client for the Garmin Connect Personal API.
//
// Authentication: the long-lived personal API key is exchanged for a JWT
// access token (valid 24 h, no refresh token). The client caches the token in
// memory only, renews it shortly before expiry and once more on HTTP 401.
package garmin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Base URLs from the official OpenAPI spec.
const (
	BaseURLGlobal = "https://personal.connectapi.garmin.com"
	BaseURLChina  = "https://personal.connectapi.garmin.cn"

	tokenPath = "/personalapitoken-service/personal-api/personal-api-key/token"
)

const (
	maxBodyBytes  = 32 << 20 // largest response we accept
	maxTokenBytes = 16 << 10
	refreshMargin = 5 * time.Minute
	maxRetries    = 3
)

// ErrNoData means Garmin answered 204 No Content: nothing recorded for the
// requested day or range.
var ErrNoData = errors.New("garmin: no data for the requested period")

// APIError is a non-success HTTP response from Garmin.
type APIError struct {
	Method string
	Status int
	Path   string
	Body   string // truncated
}

func (e *APIError) Error() string {
	hint := ""
	switch e.Status {
	case http.StatusBadRequest:
		hint = " (Garmin rejects date ranges longer than the endpoint allows and malformed parameters)"
	case http.StatusUnauthorized:
		hint = " (the API key is invalid, expired or deleted: create a new one at https://connect.garmin.com/app/settings/personalApiToken)"
	case http.StatusForbidden:
		hint = " (the key lacks access to this resource)"
	case http.StatusTooManyRequests:
		hint = " (rate limited: wait and retry)"
	}
	method := e.Method
	if method == "" {
		method = http.MethodGet
	}
	msg := fmt.Sprintf("garmin: %s %s: HTTP %d%s", method, e.Path, e.Status, hint)
	if e.Body != "" && e.Body != "{}" {
		msg += ": " + e.Body
	}
	return msg
}

// Client talks to the Personal API. It is safe for concurrent use.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	log     *slog.Logger
	ua      string
	sem     chan struct{}
	now     func() time.Time

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API host. Intended for tests.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient replaces the HTTP client. Its CheckRedirect is overridden.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithMaxConcurrency limits parallel requests to Garmin.
func WithMaxConcurrency(n int) Option {
	return func(c *Client) {
		if n > 0 {
			c.sem = make(chan struct{}, n)
		}
	}
}

// New returns a client for apiKey.
func New(apiKey string, opts ...Option) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("garmin: empty API key")
	}
	if strings.ContainsAny(apiKey, " \t\r\n") {
		return nil, errors.New("garmin: API key contains whitespace; check the env file for stray characters")
	}
	c := &Client{
		baseURL: BaseURLGlobal,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
		log:     slog.New(slog.DiscardHandler),
		ua:      "garmin-mcp",
		sem:     make(chan struct{}, 4),
		now:     time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	// Garmin never redirects these endpoints. Refusing redirects guarantees
	// the key and token are only ever sent to the configured host.
	h := *c.http
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.http = &h
	return c, nil
}

// CheckAuth exchanges the API key for a token. Use it to validate setup.
func (c *Client) CheckAuth(ctx context.Context) (expires time.Time, err error) {
	if _, err := c.bearer(ctx, true); err != nil {
		return time.Time{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokenExp, nil
}

// Get performs a GET request and returns the response body. It returns
// ErrNoData for HTTP 204 and *APIError for other non-2xx responses.
func (c *Client) Get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	reauthed := false
	for attempt := 0; ; attempt++ {
		tok, err := c.bearer(ctx, false)
		if err != nil {
			return nil, err
		}
		status, body, header, err := c.do(ctx, http.MethodGet, u, "Bearer "+tok, "application/json")
		if err != nil {
			if ctx.Err() != nil || attempt >= maxRetries {
				return nil, err
			}
			c.log.Debug("garmin request failed, retrying", "path", path, "attempt", attempt+1, "err", err)
			if err := sleep(ctx, backoff(attempt, "")); err != nil {
				return nil, err
			}
			continue
		}
		switch {
		case status == http.StatusOK:
			return body, nil
		case status == http.StatusNoContent:
			return nil, ErrNoData
		case status == http.StatusUnauthorized && !reauthed:
			// The token may have been revoked early; get a new one once.
			reauthed = true
			c.invalidate(tok)
			continue
		case (status == http.StatusTooManyRequests || status >= 500) && attempt < maxRetries:
			c.log.Debug("garmin transient error, retrying", "path", path, "status", status, "attempt", attempt+1)
			if err := sleep(ctx, backoff(attempt, header.Get("Retry-After"))); err != nil {
				return nil, err
			}
			continue
		}
		return nil, &APIError{Status: status, Path: path, Body: truncate(string(body), 300)}
	}
}

// do sends one request and reads a bounded body.
func (c *Client) do(ctx context.Context, method, u, auth, accept string) (int, []byte, http.Header, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", c.ua)

	start := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("garmin: %s %s: %w", method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("garmin: read response of %s: %w", req.URL.Path, err)
	}
	if len(body) > maxBodyBytes {
		return 0, nil, nil, fmt.Errorf("garmin: response of %s exceeds %d bytes", req.URL.Path, maxBodyBytes)
	}
	c.log.Debug("garmin request", "method", method, "path", req.URL.Path,
		"status", resp.StatusCode, "bytes", len(body), "duration", c.now().Sub(start))
	return resp.StatusCode, body, resp.Header, nil
}

// bearer returns a valid access token, fetching a new one when needed.
func (c *Client) bearer(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && c.now().Add(refreshMargin).Before(c.tokenExp) {
		return c.token, nil
	}
	tok, exp, err := c.fetchToken(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.tokenExp = tok, exp
	return tok, nil
}

func (c *Client) invalidate(tok string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == tok {
		c.token = ""
	}
}

func (c *Client) fetchToken(ctx context.Context) (string, time.Time, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt-1, "")); err != nil {
				return "", time.Time{}, err
			}
		}
		// The token endpoint answers text/plain and rejects Accept: application/json with 406.
		status, body, _, err := c.do(ctx, http.MethodPost, c.baseURL+tokenPath, "ApiKey "+c.apiKey, "text/plain, */*")
		if err != nil {
			if ctx.Err() != nil {
				return "", time.Time{}, err
			}
			lastErr = err
			continue
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			lastErr = &APIError{Method: http.MethodPost, Status: status, Path: tokenPath}
			continue
		}
		if status != http.StatusOK {
			// Never echo the body: it is unknown content from an auth endpoint.
			return "", time.Time{}, &APIError{Method: http.MethodPost, Status: status, Path: tokenPath}
		}
		if len(body) > maxTokenBytes {
			return "", time.Time{}, errors.New("garmin: token response too large")
		}
		tok := strings.TrimSpace(string(body))
		exp, err := jwtExpiry(tok)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("garmin: unexpected token format: %w", err)
		}
		c.log.Debug("garmin access token obtained", "expires", exp)
		return tok, exp, nil
	}
	return "", time.Time{}, fmt.Errorf("garmin: obtain access token: %w", lastErr)
}

// jwtExpiry reads the exp claim without verifying the signature. The token is
// only used to schedule renewal; Garmin verifies it on every request.
func jwtExpiry(tok string) (time.Time, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("not a JWT")
	}
	for _, p := range parts {
		if p == "" || strings.ContainsFunc(p, func(r rune) bool {
			return (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_'
		}) {
			return time.Time{}, errors.New("not a JWT")
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, errors.New("bad JWT payload encoding")
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, errors.New("JWT has no exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}

func backoff(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s > 0 {
		return min(time.Duration(s)*time.Second, 30*time.Second)
	}
	d := 500 * time.Millisecond << attempt
	return d + rand.N(d/2) //nolint:gosec // retry jitter, not security-sensitive
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
