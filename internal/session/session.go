package session

import (
	"compress/gzip"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/playwright-community/playwright-go"
)

const BaseURL = "https://bidplus.gem.gov.in"

const UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

// SessionPair holds one CSRF token + HTTP client with cookie jar
type SessionPair struct {
	CSRFToken string
	Client    *http.Client
}

// SessionPool rotates through multiple session pairs (thread-safe).
type SessionPool struct {
	pairs []*SessionPair
	idx   uint64
}

// Next returns the next session pair in round-robin fashion (thread-safe).
func (p *SessionPool) Next() *SessionPair {
	i := atomic.AddUint64(&p.idx, 1)
	return p.pairs[i%uint64(len(p.pairs))]
}

// Len returns the number of sessions in the pool.
func (p *SessionPool) Len() int {
	return len(p.pairs)
}

// BootstrapSessions creates multiple authenticated session pairs.
func BootstrapSessions(count int) (*SessionPool, error) {
	log.Printf("Bootstrapping %d sessions...", count)

	pool := &SessionPool{}

	for i := range count {
		var sp *SessionPair
		var err error
		for attempt := 1; attempt <= 5; attempt++ {
			sp, err = createSessionAuto()
			if err == nil {
				break
			}
			log.Printf("Session %d attempt %d failed: %v", i+1, attempt, err)
			if attempt < 5 {
				backoff := time.Duration(attempt*attempt*5) * time.Second // 5s, 20s, 45s, 80s
				log.Printf("Retrying in %s...", backoff)
				time.Sleep(backoff)
			}
		}
		if err != nil {
			if len(pool.pairs) > 0 {
				log.Printf("Session %d failed after retries, continuing with %d sessions", i+1, len(pool.pairs))
				continue
			}
			return nil, fmt.Errorf("no sessions could be created: %w", err)
		}
		pool.pairs = append(pool.pairs, sp)
		log.Printf("Session %d/%d: CSRF=%s", i+1, count, sp.CSRFToken)

		if i < count-1 {
			time.Sleep(5 * time.Second) // longer gap between sessions to avoid WAF triggers
		}
	}

	log.Printf("Successfully created %d sessions", len(pool.pairs))
	return pool, nil
}

// newGEMTransport creates an HTTP transport configured for GEM API access.
// Forces HTTP/1.1 to avoid TLS fingerprint issues with Go's default h2/h3 ALPN negotiation.
func newGEMTransport() *http.Transport {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	// Disable HTTP/2 — forces HTTP/1.1 ALPN, matching Go 1.24 behavior
	transport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	return transport
}

func createSession() (*SessionPair, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Jar:       jar,
		Transport: newGEMTransport(),
	}

	req, err := http.NewRequest("GET", BaseURL+"/all-bids", nil)
	if err != nil {
		return nil, err
	}

	// Must match browser headers exactly — WAF checks Accept-Encoding pattern
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET main page: %w", err)
	}
	defer resp.Body.Close()

	body, err := ReadResponseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	csrfRegex := regexp.MustCompile(`csrf_bd_gem_nk['\"]?\s*[:=]\s*['\"]([^'\"]+)['\"]`)
	matches := csrfRegex.FindSubmatch(body)
	if len(matches) < 2 {
		return nil, fmt.Errorf("could not extract CSRF token from page")
	}

	return &SessionPair{
		CSRFToken: string(matches[1]),
		Client:    client,
	}, nil
}

// createSessionPlaywright uses a real Chromium browser to bootstrap a session.
func createSessionPlaywright() (*SessionPair, error) {
	// Auto-install Chromium if not present (no-op if already installed)
	if err := playwright.Install(&playwright.RunOptions{
		Browsers: []string{"chromium"},
	}); err != nil {
		return nil, fmt.Errorf("playwright install: %w", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("playwright start: %w", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("browser launch: %w", err)
	}
	defer browser.Close()

	context, err := browser.NewContext(playwright.BrowserNewContextOptions{
		UserAgent: playwright.String(UserAgent),
	})
	if err != nil {
		return nil, fmt.Errorf("browser context: %w", err)
	}
	defer context.Close()

	page, err := context.NewPage()
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}

	// Load the main page to get WAF cookies and CSRF
	if _, err := page.Goto(BaseURL+"/all-bids", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
		Timeout:   playwright.Float(30000),
	}); err != nil {
		return nil, fmt.Errorf("goto all-bids: %w", err)
	}

	// Extract CSRF token from page JS context
	csrfVal, err := page.Evaluate(`() => {
		// Try hidden input
		const input = document.querySelector('input[name="csrf_bd_gem_nk"]');
		if (input) return input.value;
		// Try JS variable in script tags
		const scripts = document.querySelectorAll('script');
		for (const s of scripts) {
			const m = s.textContent.match(/csrf_bd_gem_nk['"]\s*[:=]\s*['"]([a-f0-9]+)['"]/);
			if (m) return m[1];
		}
		return null;
	}`)
	if err != nil {
		return nil, fmt.Errorf("evaluate csrf: %w", err)
	}
	csrf, ok := csrfVal.(string)
	if !ok || csrf == "" {
		return nil, fmt.Errorf("could not extract CSRF token from page (playwright)")
	}

	// Extract cookies from browser context
	browserCookies, err := context.Cookies(BaseURL)
	if err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}

	// Build a net/http client with the same cookies
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}

	parsedURL, _ := url.Parse(BaseURL)
	var httpCookies []*http.Cookie
	for _, c := range browserCookies {
		httpCookies = append(httpCookies, &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HttpOnly: c.HttpOnly,
		})
	}
	jar.SetCookies(parsedURL, httpCookies)

	client := &http.Client{
		Timeout:   30 * time.Second,
		Jar:       jar,
		Transport: newGEMTransport(),
	}

	log.Printf("[playwright] Session created: CSRF=%s..., %d cookies transferred",
		csrf[:min(16, len(csrf))], len(httpCookies))

	return &SessionPair{
		CSRFToken: csrf,
		Client:    client,
	}, nil
}

// createSessionAuto tries Playwright first (real browser, reliable WAF bypass),
// falls back to raw HTTP if Playwright is unavailable or fails.
func createSessionAuto() (*SessionPair, error) {
	sp, err := createSessionPlaywright()
	if err == nil {
		return sp, nil
	}
	log.Printf("[session] Playwright failed: %v — falling back to raw HTTP", err)
	return createSession()
}

// SetAjaxHeaders sets common headers for GEM AJAX requests.
func SetAjaxHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Origin", BaseURL)
	req.Header.Set("Referer", BaseURL+"/all-bids")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
}

// ReadResponseBody reads the response body, handling gzip if needed.
// Returns an error if status is not 200.
func ReadResponseBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	return io.ReadAll(reader)
}
