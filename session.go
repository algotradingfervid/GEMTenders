package main

import (
	"compress/gzip"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"time"
)

const baseURL = "https://bidplus.gem.gov.in"

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

// SessionPair holds one CSRF token + HTTP client with cookie jar
type SessionPair struct {
	CSRFToken string
	Client    *http.Client
}

// SessionPool rotates through multiple session pairs
type SessionPool struct {
	Pairs []*SessionPair
	idx   int
}

func (p *SessionPool) Next() *SessionPair {
	sp := p.Pairs[p.idx%len(p.Pairs)]
	p.idx++
	return sp
}

func BootstrapSessions(count int) (*SessionPool, error) {
	log.Printf("Bootstrapping %d sessions...", count)

	pool := &SessionPool{}

	for i := 0; i < count; i++ {
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
			if len(pool.Pairs) > 0 {
				log.Printf("Session %d failed after retries, continuing with %d sessions", i+1, len(pool.Pairs))
				continue
			}
			return nil, fmt.Errorf("no sessions could be created: %w", err)
		}
		pool.Pairs = append(pool.Pairs, sp)
		log.Printf("Session %d/%d: CSRF=%s", i+1, count, sp.CSRFToken)

		if i < count-1 {
			time.Sleep(5 * time.Second) // longer gap between sessions to avoid WAF triggers
		}
	}

	log.Printf("Successfully created %d sessions", len(pool.Pairs))
	return pool, nil
}

func createSession() (*SessionPair, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}

	// Custom transport: force HTTP/1.1 to avoid TLS fingerprint issues
	// with Go 1.25's default h2/h3 ALPN negotiation that GEM's WAF rejects.
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

	client := &http.Client{
		Timeout:   30 * time.Second,
		Jar:       jar,
		Transport: transport,
	}

	req, err := http.NewRequest("GET", baseURL+"/all-bids", nil)
	if err != nil {
		return nil, err
	}

	// Must match browser headers exactly — WAF checks Accept-Encoding pattern
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET main page: %w", err)
	}
	defer resp.Body.Close()

	// Handle gzip manually since we set Accept-Encoding explicitly
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	body, err := io.ReadAll(reader)
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
