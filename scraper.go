package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

func DefaultPayload(page int) map[string]interface{} {
	payload := map[string]interface{}{
		"param": map[string]interface{}{
			"searchBid":  "",
			"searchType": "fullText",
		},
		"filter": map[string]interface{}{
			"bidStatusType": "ongoing_bids",
			"byType":        "all",
			"highBidValue":  "",
			"byEndDate":     map[string]string{"from": "", "to": ""},
			"sort":          "Bid-End-Date-Oldest",
		},
	}
	if page > 1 {
		payload["page"] = page
	}
	return payload
}

func ScrapeBids(pool *SessionPool, db *sql.DB, workers int, rps int) error {
	// First request to get total count
	sp := pool.Next()
	log.Println("Fetching page 1 to get total count...")

	apiResp, err := fetchPage(sp, 1)
	if err != nil {
		return fmt.Errorf("page 1: %w", err)
	}

	if len(apiResp.Response.Response.Docs) > 0 {
		InsertBidsBatch(db, apiResp.Response.Response.Docs)
	}

	totalFound := apiResp.Response.Response.NumFound
	totalPages := (totalFound + 9) / 10
	log.Printf("Total records: %d, Total pages: %d, Workers: %d, Rate: %d req/s",
		totalFound, totalPages, workers, rps)

	// Rate limiter: rps requests per second with burst of rps*2
	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)

	// Queue ALL pages — duplicates handled by INSERT OR IGNORE
	pages := make(chan int, totalPages)
	for p := 2; p <= totalPages; p++ {
		pages <- p
	}
	close(pages)

	var (
		wg      sync.WaitGroup
		scraped int64
		errors  int64
		mu      sync.Mutex
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			sp := pool.Next()

			for page := range pages {
				// Rate limit
				limiter.Wait(context.Background())

				resp, err := fetchPage(sp, page)
				if err != nil {
					// Retry with different session after backoff
					time.Sleep(3 * time.Second)
					sp = pool.Next()
					limiter.Wait(context.Background())
					resp, err = fetchPage(sp, page)
					if err != nil {
						log.Printf("[W%d] Page %d failed: %v", workerID, page, err)
						atomic.AddInt64(&errors, 1)
						continue
					}
				}

				docs := resp.Response.Response.Docs
				if len(docs) > 0 {
					mu.Lock()
					inserted, _ := InsertBidsBatch(db, docs)
					mu.Unlock()
					atomic.AddInt64(&scraped, int64(inserted))
				}

				done := atomic.LoadInt64(&scraped)
				if done > 0 && done%500 == 0 {
					log.Printf("Progress: %d bids scraped, %d errors", done, atomic.LoadInt64(&errors))
				}
			}
		}(w)
	}

	wg.Wait()

	total, _, _ := GetBidCount(db)
	log.Printf("Scraping complete. Total bids in DB: %d, Errors: %d", total, errors)
	return nil
}

func fetchPage(sp *SessionPair, page int) (*APIResponse, error) {
	payload := DefaultPayload(page)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	formData := url.Values{}
	formData.Set("payload", string(payloadJSON))
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	req, err := http.NewRequest("POST", baseURL+"/all-bids-data",
		strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,hi;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Host", "bidplus.gem.gov.in")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/all-bids")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w (body: %.200s)", err, string(body))
	}

	if apiResp.Code != 200 {
		return nil, fmt.Errorf("api error: code=%d msg=%s", apiResp.Code, apiResp.Message)
	}

	return &apiResp, nil
}
