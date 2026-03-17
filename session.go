package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const baseURL = "https://bidplus.gem.gov.in"

func NewSession() (*Session, error) {
	log.Println("Bootstrapping session via headless Chrome...")

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Set timeout for the whole operation
	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var csrfToken string
	var pageHTML string

	// Navigate and extract CSRF token
	err := chromedp.Run(ctx,
		chromedp.Navigate(baseURL+"/all-bids"),
		chromedp.WaitReady("body"),
		chromedp.Sleep(3*time.Second),
		chromedp.OuterHTML("html", &pageHTML),
	)
	if err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}

	// Extract CSRF token from HTML
	csrfToken = extractCSRF(pageHTML)
	if csrfToken == "" {
		return nil, fmt.Errorf("could not extract CSRF token from page")
	}
	log.Printf("CSRF token: %s", csrfToken)

	// Extract cookies
	var chromeCookies []*network.Cookie
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			cookies, err := network.GetCookies().Do(ctx)
			if err != nil {
				return err
			}
			chromeCookies = cookies
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}

	// Convert chrome cookies to http.Cookie
	httpCookies := make([]*http.Cookie, len(chromeCookies))
	for i, c := range chromeCookies {
		httpCookies[i] = &http.Cookie{
			Name:   c.Name,
			Value:  c.Value,
			Domain: c.Domain,
			Path:   c.Path,
		}
	}
	log.Printf("Extracted %d cookies", len(httpCookies))

	return &Session{
		CSRFToken: csrfToken,
		Cookies:   httpCookies,
	}, nil
}

func extractCSRF(html string) string {
	// The CSRF token appears in the JS as part of the AJAX call:
	// csrf_bd_gem_nk=<token>
	markers := []string{"csrf_bd_gem_nk"}
	for _, marker := range markers {
		idx := strings.Index(html, marker)
		if idx == -1 {
			continue
		}
		// Look for the value after the marker
		rest := html[idx+len(marker):]
		// Skip delimiter characters (=, ', ", :, space)
		start := -1
		for i, c := range rest {
			if c != '=' && c != '\'' && c != '"' && c != ':' && c != ' ' {
				start = i
				break
			}
		}
		if start == -1 {
			continue
		}
		// Read until delimiter
		end := start
		for end < len(rest) {
			c := rest[end]
			if c == '"' || c == '\'' || c == '&' || c == '<' || c == ' ' || c == '}' || c == ',' {
				break
			}
			end++
		}
		token := rest[start:end]
		if len(token) > 10 {
			return token
		}
	}
	return ""
}
