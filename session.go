package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const baseURL = "https://bidplus.gem.gov.in"

// BrowserSession keeps a live browser context for all requests.
// The F5 WAF on gem.gov.in binds cookies to the TLS session,
// so all requests must go through the same browser instance.
type BrowserSession struct {
	CSRFToken   string
	Ctx         context.Context
	CancelFunc  context.CancelFunc
	AllocCancel context.CancelFunc
}

func NewBrowserSession() (*BrowserSession, error) {
	log.Println("Bootstrapping session via headless Chrome...")

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	ctx, cancel := chromedp.NewContext(allocCtx)

	var pageHTML string

	err := chromedp.Run(ctx,
		chromedp.Navigate(baseURL+"/all-bids"),
		chromedp.WaitReady("body"),
		chromedp.Sleep(3*time.Second),
		chromedp.OuterHTML("html", &pageHTML),
	)
	if err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("navigate: %w", err)
	}

	csrfToken := extractCSRF(pageHTML)
	if csrfToken == "" {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("could not extract CSRF token from page")
	}
	log.Printf("CSRF token: %s", csrfToken)

	return &BrowserSession{
		CSRFToken:   csrfToken,
		Ctx:         ctx,
		CancelFunc:  cancel,
		AllocCancel: allocCancel,
	}, nil
}

func (s *BrowserSession) Close() {
	s.CancelFunc()
	s.AllocCancel()
}

// evalAsyncJS evaluates an async JS expression in the browser and returns the string result.
// Uses CDP runtime.Evaluate with AwaitPromise to properly handle async/await.
func (s *BrowserSession) evalAsyncJS(jsExpr string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(s.Ctx, timeout)
	defer cancel()

	var result string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		res, exceptionDetails, err := runtime.Evaluate(jsExpr).
			WithAwaitPromise(true).
			WithReturnByValue(true).
			Do(ctx)
		if err != nil {
			return fmt.Errorf("runtime.Evaluate: %w", err)
		}
		if exceptionDetails != nil {
			return fmt.Errorf("JS exception: %s", exceptionDetails.Text)
		}
		// res.Value is a JSON-encoded value; for a string it includes quotes
		if err := json.Unmarshal(res.Value, &result); err != nil {
			return fmt.Errorf("unmarshal result: %w (raw: %s)", err, string(res.Value))
		}
		return nil
	}))
	return result, err
}

// FetchBidsPage calls the bids API from within the browser context
func (s *BrowserSession) FetchBidsPage(page int) (*APIResponse, error) {
	payload := DefaultPayload(page)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	js := fmt.Sprintf(`
		(async () => {
			const formData = new URLSearchParams();
			formData.append('payload', %s);
			formData.append('csrf_bd_gem_nk', %s);
			const resp = await fetch('/all-bids-data', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/x-www-form-urlencoded',
					'X-Requested-With': 'XMLHttpRequest'
				},
				body: formData.toString()
			});
			return await resp.text();
		})()
	`, jsonQuote(string(payloadJSON)), jsonQuote(s.CSRFToken))

	resultStr, err := s.evalAsyncJS(js, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("fetch bids: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal([]byte(resultStr), &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (body: %.200s)", err, resultStr)
	}

	return &apiResp, nil
}

// DownloadPDFBytes downloads a PDF via the browser and returns the raw bytes
func (s *BrowserSession) DownloadPDFBytes(bidIDParent int) ([]byte, error) {
	pdfURL := fmt.Sprintf("/showbidDocument/%d", bidIDParent)

	js := fmt.Sprintf(`
		(async () => {
			const resp = await fetch(%s);
			if (!resp.ok) throw new Error('HTTP ' + resp.status);
			const buf = await resp.arrayBuffer();
			const bytes = new Uint8Array(buf);
			let binary = '';
			const chunkSize = 8192;
			for (let i = 0; i < bytes.length; i += chunkSize) {
				const chunk = bytes.subarray(i, i + chunkSize);
				binary += String.fromCharCode.apply(null, chunk);
			}
			return btoa(binary);
		})()
	`, jsonQuote(pdfURL))

	result, err := s.evalAsyncJS(js, 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("fetch pdf: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(result)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}

	return data, nil
}

func extractCSRF(html string) string {
	markers := []string{"csrf_bd_gem_nk"}
	for _, marker := range markers {
		idx := strings.Index(html, marker)
		if idx == -1 {
			continue
		}
		rest := html[idx+len(marker):]
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

// jsonQuote returns a JSON-encoded string literal for embedding in JS
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
