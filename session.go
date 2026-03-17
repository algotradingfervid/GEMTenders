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

// evalAsyncJS evaluates an async JS expression and returns the string result.
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
		if err := json.Unmarshal(res.Value, &result); err != nil {
			return fmt.Errorf("unmarshal result: %w (raw: %s)", err, string(res.Value))
		}
		return nil
	}))
	return result, err
}

// FetchBidsPage calls the bids API for a single page.
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

// FetchBidsPagesBatch fetches multiple pages concurrently via Promise.allSettled.
// Returns a JSON array of results (one per page). Each result is either the API
// response text or null on failure.
func (s *BrowserSession) FetchBidsPagesBatch(pages []int) ([]string, error) {
	if len(pages) == 0 {
		return nil, nil
	}

	// Build an array of payloads
	var payloadJSONs []string
	for _, page := range pages {
		p := DefaultPayload(page)
		b, _ := json.Marshal(p)
		payloadJSONs = append(payloadJSONs, string(b))
	}

	payloadsArrayJSON, _ := json.Marshal(payloadJSONs)
	csrfQuoted := jsonQuote(s.CSRFToken)

	js := fmt.Sprintf(`
		(async () => {
			const payloads = %s;
			const csrf = %s;
			const promises = payloads.map(payload => {
				const formData = new URLSearchParams();
				formData.append('payload', payload);
				formData.append('csrf_bd_gem_nk', csrf);
				return fetch('/all-bids-data', {
					method: 'POST',
					headers: {
						'Content-Type': 'application/x-www-form-urlencoded',
						'X-Requested-With': 'XMLHttpRequest'
					},
					body: formData.toString()
				}).then(r => r.text());
			});
			const results = await Promise.allSettled(promises);
			return JSON.stringify(results.map(r =>
				r.status === 'fulfilled' ? r.value : null
			));
		})()
	`, string(payloadsArrayJSON), csrfQuoted)

	// Longer timeout for batch: 30s base + 2s per page
	timeout := time.Duration(30+len(pages)*2) * time.Second
	resultStr, err := s.evalAsyncJS(js, timeout)
	if err != nil {
		return nil, fmt.Errorf("batch fetch: %w", err)
	}

	var results []string
	if err := json.Unmarshal([]byte(resultStr), &results); err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}

	return results, nil
}

// DownloadPDFBytes downloads a single PDF via the browser.
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

// DownloadPDFBatch downloads multiple PDFs concurrently via Promise.allSettled.
// Returns a map of bidIDParent -> base64 data (nil value on failure).
func (s *BrowserSession) DownloadPDFBatch(ids []int) (map[int][]byte, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	idsJSON, _ := json.Marshal(ids)

	js := fmt.Sprintf(`
		(async () => {
			const ids = %s;
			const promises = ids.map(id => {
				return fetch('/showbidDocument/' + id)
					.then(async resp => {
						if (!resp.ok) throw new Error('HTTP ' + resp.status);
						const buf = await resp.arrayBuffer();
						const bytes = new Uint8Array(buf);
						let binary = '';
						const chunkSize = 8192;
						for (let i = 0; i < bytes.length; i += chunkSize) {
							const chunk = bytes.subarray(i, i + chunkSize);
							binary += String.fromCharCode.apply(null, chunk);
						}
						return { id: id, data: btoa(binary) };
					});
			});
			const results = await Promise.allSettled(promises);
			return JSON.stringify(results.map((r, i) => {
				if (r.status === 'fulfilled') return r.value;
				return { id: ids[i], data: null, error: r.reason ? r.reason.message : 'unknown' };
			}));
		})()
	`, string(idsJSON))

	// Timeout: 60s base + 3s per PDF
	timeout := time.Duration(60+len(ids)*3) * time.Second
	resultStr, err := s.evalAsyncJS(js, timeout)
	if err != nil {
		return nil, fmt.Errorf("batch download: %w", err)
	}

	var results []struct {
		ID    int    `json:"id"`
		Data  string `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(resultStr), &results); err != nil {
		return nil, fmt.Errorf("unmarshal batch: %w", err)
	}

	out := make(map[int][]byte)
	for _, r := range results {
		if r.Data == "" {
			if r.Error != "" {
				log.Printf("Download failed for %d: %s", r.ID, r.Error)
			}
			continue
		}
		data, err := base64.StdEncoding.DecodeString(r.Data)
		if err != nil {
			log.Printf("Decode failed for %d: %v", r.ID, err)
			continue
		}
		out[r.ID] = data
	}

	return out, nil
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

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
