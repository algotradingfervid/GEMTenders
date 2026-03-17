package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"log"
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
		for attempt := 1; attempt <= 3; attempt++ {
			sp, err = createSession()
			if err == nil {
				break
			}
			log.Printf("Session %d attempt %d failed: %v", i+1, attempt, err)
			if attempt < 3 {
				backoff := time.Duration(attempt*10) * time.Second
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
			time.Sleep(2 * time.Second)
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

	client := &http.Client{
		Timeout: 30 * time.Second,
		Jar:     jar,
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
