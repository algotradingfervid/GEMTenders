package main

import (
	"compress/gzip"
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

// ProgressFunc is a callback for reporting task progress to the ScrapeManager.
type ProgressFunc func(current, total, errors int64, msg string)

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

// ScrapeBids launches `scrapers` parallel scraper instances, each offset by `staggerSec` seconds.
// Each scraper independently scrapes all pages using its own workers and rate limiter.
// The staggered snapshots catch records that shift between pages on the live API.
func ScrapeBids(pool *SessionPool, db *sql.DB, scrapers int, staggerSec int, workersPerScraper int, rps int, errLog *ErrorLog, onProgress ProgressFunc) error {
	// First request to get total count
	sp := pool.Next()
	log.Println("Fetching page 1 to get total count...")

	apiResp, err := fetchPage(sp, 1)
	if err != nil {
		return fmt.Errorf("page 1: %w", err)
	}

	if len(apiResp.Response.Response.Docs) > 0 {
		if _, err := InsertBidsBatch(db, apiResp.Response.Response.Docs); err != nil {
			errLog.Log("scrape-insert", "page=1", err)
		}
	}

	totalFound := apiResp.Response.Response.NumFound
	totalPages := (totalFound + 9) / 10
	log.Printf("Total records: %d, Total pages: %d", totalFound, totalPages)
	log.Printf("Launching %d parallel scrapers (stagger: %ds, workers/scraper: %d, rate: %d req/s each)",
		scrapers, staggerSec, workersPerScraper, rps)

	var wg sync.WaitGroup

	for s := 0; s < scrapers; s++ {
		if s > 0 {
			log.Printf("Scraper %d starting in %ds...", s+1, staggerSec)
			time.Sleep(time.Duration(staggerSec) * time.Second)
		}

		wg.Add(1)
		go func(scraperID int) {
			defer wg.Done()
			runScraper(scraperID, pool, db, totalPages, workersPerScraper, rps, errLog, onProgress)
		}(s + 1)
	}

	wg.Wait()

	total, _, countErr := GetBidCount(db)
	if countErr != nil {
		log.Printf("[scraper] GetBidCount error: %v", countErr)
	}
	log.Printf("All scrapers complete. Total unique bids in DB: %d", total)
	return nil
}

func runScraper(scraperID int, pool *SessionPool, db *sql.DB, totalPages int, workers int, rps int, errLog *ErrorLog, onProgress ProgressFunc) {
	log.Printf("[S%d] Starting: %d pages, %d workers, %d req/s", scraperID, totalPages, workers, rps)

	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)

	pages := make(chan int, totalPages)
	for p := 1; p <= totalPages; p++ {
		pages <- p
	}
	close(pages)

	var (
		wg        sync.WaitGroup
		scraped   int64
		pagesDone int64
		errors    int64
		mu        sync.Mutex
	)

	startTime := time.Now()

	// Progress reporter goroutine
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				pd := atomic.LoadInt64(&pagesDone)
				sc := atomic.LoadInt64(&scraped)
				er := atomic.LoadInt64(&errors)
				elapsed := time.Since(startTime)
				pagesPerSec := float64(pd) / elapsed.Seconds()
				remaining := int64(totalPages) - pd
				var eta time.Duration
				if pagesPerSec > 0 {
					eta = time.Duration(float64(remaining)/pagesPerSec) * time.Second
				}
				pct := float64(pd) / float64(totalPages) * 100
				log.Printf("[S%d] %d/%d pages (%.1f%%) | %d new bids | %d errors | %.1f pages/s | ETA %s",
					scraperID, pd, totalPages, pct, sc, er, pagesPerSec, eta.Round(time.Second))
				if onProgress != nil {
					msg := fmt.Sprintf("%.1f%% — %d pages, %d new bids, ETA %s", pct, pd, sc, eta.Round(time.Second))
					onProgress(pd, int64(totalPages), er, msg)
				}
			}
		}
	}()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			sp := pool.Next()

			for page := range pages {
				limiter.Wait(context.Background())

				resp, err := fetchPage(sp, page)
				if err != nil {
					time.Sleep(3 * time.Second)
					sp = pool.Next()
					limiter.Wait(context.Background())
					resp, err = fetchPage(sp, page)
					if err != nil {
						errLog.Log("scrape", fmt.Sprintf("page=%d", page), err)
						atomic.AddInt64(&errors, 1)
						atomic.AddInt64(&pagesDone, 1)
						continue
					}
				}

				docs := resp.Response.Response.Docs
				if len(docs) > 0 {
					mu.Lock()
					inserted, insertErr := InsertBidsBatch(db, docs)
					mu.Unlock()
					if insertErr != nil {
						errLog.Log("scrape-insert", fmt.Sprintf("page=%d", page), insertErr)
					}
					atomic.AddInt64(&scraped, int64(inserted))
				}

				atomic.AddInt64(&pagesDone, 1)
			}
		}(w)
	}

	wg.Wait()
	close(done)

	elapsed := time.Since(startTime)
	total, _, countErr := GetBidCount(db)
	if countErr != nil {
		log.Printf("[scraper] GetBidCount error: %v", countErr)
	}
	log.Printf("[S%d] Done in %s: %d/%d pages, %d new bids inserted, %d errors, DB total: %d",
		scraperID, elapsed.Round(time.Second), pagesDone, totalPages, scraped, errors, total)
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
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9,hi;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("DNT", "1")
	req.Header.Set("Host", "bidplus.gem.gov.in")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", baseURL+"/all-bids")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

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

	body, err := io.ReadAll(reader)
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

// ScrapeBidsWithProgress wraps ScrapeBids with progress callbacks for the ScrapeManager.
// Uses reasonable defaults: 5 scrapers, 30s stagger, 100 workers, 50 rps.
func ScrapeBidsWithProgress(pool *SessionPool, db *sql.DB, errLog *ErrorLog, onProgress ProgressFunc) error {
	if onProgress != nil {
		onProgress(0, 0, 0, "Starting bid scrape...")
	}
	err := ScrapeBids(pool, db, 5, 30, 100, 50, errLog, onProgress)
	if err != nil {
		if onProgress != nil {
			onProgress(0, 0, 1, fmt.Sprintf("Scrape error: %v", err))
		}
		return err
	}
	if onProgress != nil {
		onProgress(0, 0, 0, "Bid scrape completed")
	}
	return nil
}
