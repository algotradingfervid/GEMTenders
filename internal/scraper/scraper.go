package scraper

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"gemtenders/internal/models"
	"gemtenders/internal/errlog"
	"gemtenders/internal/session"
	"gemtenders/internal/store"
)

const pageSize = 10 // Number of bid records per API page

func DefaultPayload(page int) map[string]any {
	payload := map[string]any{
		"param": map[string]any{
			"searchBid":  "",
			"searchType": "fullText",
		},
		"filter": map[string]any{
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
func ScrapeBids(pool *session.SessionPool, db *sql.DB, scrapers int, staggerSec int, workersPerScraper int, rps int, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) error {
	// First request to get total count
	sp := pool.Next()
	log.Println("Fetching page 1 to get total count...")

	apiResp, err := fetchPage(sp, 1)
	if err != nil {
		return fmt.Errorf("page 1: %w", err)
	}

	if len(apiResp.Response.Response.Docs) > 0 {
		if _, err := store.InsertBidsBatch(db, apiResp.Response.Response.Docs); err != nil {
			errLog.Log("scrape-insert", "page=1", err)
		}
	}

	totalFound := apiResp.Response.Response.NumFound
	totalPages := (totalFound + pageSize - 1) / pageSize
	log.Printf("Total records: %d, Total pages: %d", totalFound, totalPages)
	log.Printf("Launching %d parallel scrapers (stagger: %ds, workers/scraper: %d, rate: %d req/s each)",
		scrapers, staggerSec, workersPerScraper, rps)

	var wg sync.WaitGroup

	for s := range scrapers {
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

	total, _, countErr := store.GetBidCount(db)
	if countErr != nil {
		log.Printf("[scraper] GetBidCount error: %v", countErr)
	}
	log.Printf("All scrapers complete. Total unique bids in DB: %d", total)
	return nil
}

func runScraper(scraperID int, pool *session.SessionPool, db *sql.DB, totalPages int, workers int, rps int, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) {
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

	for w := range workers {
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
					inserted, insertErr := store.InsertBidsBatch(db, docs)
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
	total, _, countErr := store.GetBidCount(db)
	if countErr != nil {
		log.Printf("[scraper] GetBidCount error: %v", countErr)
	}
	log.Printf("[S%d] Done in %s: %d/%d pages, %d new bids inserted, %d errors, DB total: %d",
		scraperID, elapsed.Round(time.Second), pagesDone, totalPages, scraped, errors, total)
}

func fetchPage(sp *session.SessionPair, page int) (*models.APIResponse, error) {
	payload := DefaultPayload(page)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	formData := url.Values{}
	formData.Set("payload", string(payloadJSON))
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	req, err := http.NewRequest("POST", session.BaseURL+"/all-bids-data",
		strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, err
	}

	session.SetAjaxHeaders(req)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("DNT", "1")
	req.Header.Set("Host", "bidplus.gem.gov.in")
	req.Header.Set("Pragma", "no-cache")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := session.ReadResponseBody(resp)
	if err != nil {
		return nil, err
	}

	var apiResp models.APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w (body: %.200s)", err, string(body))
	}

	if apiResp.Code != 200 {
		return nil, fmt.Errorf("api error: code=%d msg=%s", apiResp.Code, apiResp.Message)
	}

	return &apiResp, nil
}

// ScrapeBidsWithProgress wraps ScrapeBids with progress callbacks for the ScrapeManager.
func ScrapeBidsWithProgress(pool *session.SessionPool, db *sql.DB, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) error {
	return models.RunWithProgress("bid scrape", onProgress, func(p models.ProgressFunc) error {
		cfg := models.DefaultScrapeConfig
		return ScrapeBids(pool, db, cfg.Scrapers, cfg.StaggerSec, cfg.Workers, cfg.RPS, errLog, p)
	})
}
