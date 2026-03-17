package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/playwright-community/playwright-go"
)

// createSessionPlaywright uses a real Chromium browser to bootstrap a session.
// This reliably bypasses GEM's WAF since it presents a genuine browser TLS
// fingerprint and JS execution environment. After extracting cookies and CSRF,
// it transfers them to a standard net/http client and closes the browser.
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
		UserAgent: playwright.String(userAgent),
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
	if _, err := page.Goto(baseURL+"/all-bids", playwright.PageGotoOptions{
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
	browserCookies, err := context.Cookies(baseURL)
	if err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}

	// Build a net/http client with the same cookies
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}

	parsedURL, _ := url.Parse(baseURL)
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

	// Use the same transport config as the raw HTTP fallback
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
	transport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)

	client := &http.Client{
		Timeout:   30 * time.Second,
		Jar:       jar,
		Transport: transport,
	}

	log.Printf("[playwright] Session created: CSRF=%s..., %d cookies transferred",
		csrf[:min(16, len(csrf))], len(httpCookies))

	return &SessionPair{
		CSRFToken: csrf,
		Client:    client,
	}, nil
}
