package scraper

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"gemtenders/internal/errlog"
	"gemtenders/internal/models"
	"gemtenders/internal/session"
	"gemtenders/internal/store"
)

var (
	// Matches: /bidding/bid/showcorrigendumpdf/{corrigendum_id}/{bid_id}
	reCorrigendumPDFLink = regexp.MustCompile(`href="(/bidding/bid/showcorrigendumpdf/(\d+)/(\d+))"`)

	// Matches: Modified On: </strong><span>2026-03-11 12:47:58</span>
	reModifiedOn = regexp.MustCompile(`id=span_(\d+)>.*?Modified On:.*?<span>([\d\- :]+)</span>`)

	// Matches: Bid extended to <strong>2026-03-18 09:00:00</strong>
	reBidExtended = regexp.MustCompile(`Bid extended to\s*<strong>([\d\- :]+)</strong>`)

	// Matches: <div class="well">
	reWellBlock = regexp.MustCompile(`<div class="well">`)
)

// ParseCorrigendumDocs extracts download links from corrigendum HTML.
func ParseCorrigendumDocs(html string, bidID int) []models.CorrigendumDoc {
	var docs []models.CorrigendumDoc

	// Pre-extract all modified-on matches into a map to avoid N*M regex work
	modMap := make(map[string]string)
	for _, mod := range reModifiedOn.FindAllStringSubmatch(html, -1) {
		modMap[mod[1]] = strings.TrimSpace(mod[2])
	}

	linkMatches := reCorrigendumPDFLink.FindAllStringSubmatch(html, -1)
	for _, m := range linkMatches {
		corrID, _ := strconv.Atoi(m[2])
		doc := models.CorrigendumDoc{
			BidID:         bidID,
			CorrigendumID: corrID,
			DownloadURL:   m[1],
		}

		// Look up modified_on from pre-built map
		doc.ModifiedOn = modMap[m[2]]

		docs = append(docs, doc)
	}

	return docs
}

// ParseLatestEndDate extracts the latest "Bid extended to" date from corrigendum HTML.
// Returns empty string if no extension found.
func ParseLatestEndDate(html string) string {
	matches := reBidExtended.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return ""
	}

	// Dates are in "YYYY-MM-DD HH:MM:SS" format — string comparison works for finding latest
	latest := ""
	for _, m := range matches {
		date := strings.TrimSpace(m[1])
		if date > latest {
			latest = date
		}
	}
	return latest
}

// ParseCorrigendumCount counts the number of corrigendum entries (well blocks) in the HTML.
func ParseCorrigendumCount(html string) int {
	return len(reWellBlock.FindAllString(html, -1))
}

// FetchOtherDetails calls POST /public-bid-other-details/{bid_id}
// Returns whether corrigendum and representation exist for this bid.
func FetchOtherDetails(sp *session.SessionPair, bidID int) (hasCorr bool, hasRepr bool, err error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/public-bid-other-details/%d", session.BaseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return false, false, err
	}
	session.SetAjaxHeaders(req)

	resp, err := sp.Client.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := session.ReadResponseBody(resp)
	if err != nil {
		return false, false, err
	}

	var apiResp models.OtherDetailsResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return false, false, fmt.Errorf("unmarshal: %w", err)
	}

	return apiResp.Response.Corrigendum, apiResp.Response.Representation, nil
}

// FetchCorrigendumHTML calls POST /bidding/bid/viewCorrigendum/{bid_id}
// Returns the raw HTML response body.
func FetchCorrigendumHTML(sp *session.SessionPair, bidID int) (string, error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/bidding/bid/viewCorrigendum/%d", session.BaseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	session.SetAjaxHeaders(req)
	req.Header.Set("Accept", "text/html, */*; q=0.01")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := session.ReadResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// FetchRepresentationHTML calls GET /publish-representations/{bid_id}
// Returns the raw HTML response body.
func FetchRepresentationHTML(sp *session.SessionPair, bidID int) (string, error) {
	reqURL := fmt.Sprintf("%s/publish-representations/%d", session.BaseURL, bidID)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", session.BaseURL+"/all-bids")
	req.Header.Set("User-Agent", session.UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := session.ReadResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// ScrapeCorrigendums checks all active bids for corrigendum/representation updates.
func ScrapeCorrigendums(pool *session.SessionPool, db *sql.DB, workers int, rps int, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) error {
	bidIDs, err := store.GetActiveBidIDs(db)
	if err != nil {
		return fmt.Errorf("get active bids: %w", err)
	}

	if len(bidIDs) == 0 {
		log.Println("[corrigendum] No active bids to check")
		return nil
	}

	totalBids := int64(len(bidIDs))
	log.Printf("[corrigendum] Checking %d active bids (%d workers, %d req/s)", totalBids, workers, rps)

	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)

	jobs := make(chan int, len(bidIDs))
	for _, id := range bidIDs {
		jobs <- id
	}
	close(jobs)

	var (
		wg      sync.WaitGroup
		checked int64
		updated int64
		errors  int64
	)

	startTime := time.Now()

	// Progress reporter goroutine
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ch := atomic.LoadInt64(&checked)
				up := atomic.LoadInt64(&updated)
				er := atomic.LoadInt64(&errors)
				elapsed := time.Since(startTime)
				bidsPerSec := float64(ch+er) / elapsed.Seconds()
				remaining := totalBids - ch - er
				var eta time.Duration
				if bidsPerSec > 0 {
					eta = time.Duration(float64(remaining)/bidsPerSec) * time.Second
				}
				pct := float64(ch+er) / float64(totalBids) * 100
				log.Printf("[corrigendum] %d/%d (%.1f%%) | %d updated | %d errors | %.1f bids/s | ETA %s",
					ch+er, totalBids, pct, up, er, bidsPerSec, eta.Round(time.Second))
				if onProgress != nil {
					msg := fmt.Sprintf("%.1f%% — %d/%d bids checked, %d updated, ETA %s", pct, ch+er, totalBids, up, eta.Round(time.Second))
					onProgress(ch+er, totalBids, er, msg)
				}
			}
		}
	}()

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sp := pool.Next()

			for bidID := range jobs {
				limiter.Wait(context.Background())

				changed, err := processOneBid(sp, db, bidID, limiter)
				if err != nil {
					errLog.Log("corrigendum-check", bidID, err)
					atomic.AddInt64(&errors, 1)
					sp = pool.Next()
					continue
				}

				if changed {
					atomic.AddInt64(&updated, 1)
				}

				atomic.AddInt64(&checked, 1)
			}
		}()
	}

	wg.Wait()
	close(done)

	elapsed := time.Since(startTime)
	log.Printf("[corrigendum] Done in %s: %d checked, %d updated, %d errors",
		elapsed.Round(time.Second), checked, updated, errors)
	return nil
}

// ScrapeCorrigenumsWithProgress wraps ScrapeCorrigendums with progress callbacks.
func ScrapeCorrigenumsWithProgress(pool *session.SessionPool, db *sql.DB, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) error {
	return models.RunWithProgress("corrigendum scrape", onProgress, func(p models.ProgressFunc) error {
		cfg := models.DefaultScrapeConfig
		return ScrapeCorrigendums(pool, db, cfg.Workers, cfg.RPS, errLog, p)
	})
}

func processOneBid(sp *session.SessionPair, db *sql.DB, bidID int, limiter *rate.Limiter) (changed bool, err error) {
	// Step 1: Check flags
	hasCorr, hasRepr, err := FetchOtherDetails(sp, bidID)
	if err != nil {
		return false, fmt.Errorf("bid %d other-details: %w", bidID, err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	// Load existing record (may not exist)
	existing, _ := store.GetBidOtherDetails(db, bidID)

	details := models.BidOtherDetails{
		BidID:             bidID,
		HasCorrigendum:    models.BoolToInt(hasCorr),
		HasRepresentation: models.BoolToInt(hasRepr),
		LastChecked:       now,
	}

	// Carry forward existing HTML if not re-fetching
	if existing != nil {
		details.CorrigendumHTML = existing.CorrigendumHTML
		details.RepresentationHTML = existing.RepresentationHTML
		details.CorrigendumCount = existing.CorrigendumCount
		details.LatestEndDate = existing.LatestEndDate
	}

	// Step 2: Fetch corrigendum if flagged
	if hasCorr {
		limiter.Wait(context.Background())
		html, err := FetchCorrigendumHTML(sp, bidID)
		if err != nil {
			return false, fmt.Errorf("bid %d corrigendum: %w", bidID, err)
		}

		// Delta detection: only process if HTML changed
		if existing == nil || html != existing.CorrigendumHTML {
			changed = true
			details.CorrigendumHTML = html
			details.CorrigendumCount = ParseCorrigendumCount(html)

			// Extract and insert document links
			docs := ParseCorrigendumDocs(html, bidID)
			for _, doc := range docs {
				if err := store.InsertCorrigendumDoc(db, doc); err != nil {
					log.Printf("[corrigendum] insert doc error bid=%d corr=%d: %v", bidID, doc.CorrigendumID, err)
				}
			}

			// Update bid end_date if extended
			latestDate := ParseLatestEndDate(html)
			if latestDate != "" {
				details.LatestEndDate = latestDate
				if err := store.UpdateBidEndDate(db, bidID, latestDate); err != nil {
					log.Printf("[corrigendum] update end_date error bid=%d date=%s: %v", bidID, latestDate, err)
				}
			}
		}
	}

	// Step 3: Fetch representation if flagged
	if hasRepr {
		limiter.Wait(context.Background())
		html, err := FetchRepresentationHTML(sp, bidID)
		if err != nil {
			return false, fmt.Errorf("bid %d representation: %w", bidID, err)
		}

		if existing == nil || html != existing.RepresentationHTML {
			changed = true
			details.RepresentationHTML = html
		}
	}

	// Step 4: Upsert
	if err := store.UpsertBidOtherDetails(db, details); err != nil {
		return false, fmt.Errorf("bid %d upsert: %w", bidID, err)
	}

	return changed, nil
}
