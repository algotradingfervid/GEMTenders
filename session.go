package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"time"
)

const baseURL = "https://bidplus.gem.gov.in"

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

// BootstrapSessions creates N sessions by visiting the main page,
// collecting cookies via cookiejar, and extracting the CSRF token.
// No browser needed — standard Go HTTP client.
func BootstrapSessions(count int) (*SessionPool, error) {
	log.Printf("Bootstrapping %d sessions...", count)

	pool := &SessionPool{}

	for i := 0; i < count; i++ {
		sp, err := createSession()
		if err != nil {
			log.Printf("Session %d failed: %v", i+1, err)
			if len(pool.Pairs) > 0 {
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

	// Visit the main page to collect cookies
	req, err := http.NewRequest("GET", baseURL+"/all-bids", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET main page: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Extract CSRF token from page HTML
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
