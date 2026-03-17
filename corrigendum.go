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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
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
func ParseCorrigendumDocs(html string, bidID int) []CorrigendumDoc {
	var docs []CorrigendumDoc

	linkMatches := reCorrigendumPDFLink.FindAllStringSubmatch(html, -1)
	for _, m := range linkMatches {
		corrID, _ := strconv.Atoi(m[2])
		doc := CorrigendumDoc{
			BidID:         bidID,
			CorrigendumID: corrID,
			DownloadURL:   m[1],
		}

		// Find modified_on for this corrigendum_id by looking for span_{id}
		modMatches := reModifiedOn.FindAllStringSubmatch(html, -1)
		for _, mod := range modMatches {
			if mod[1] == m[2] {
				doc.ModifiedOn = strings.TrimSpace(mod[2])
				break
			}
		}

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
func FetchOtherDetails(sp *SessionPair, bidID int) (hasCorr bool, hasRepr bool, err error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/public-bid-other-details/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return false, false, err
	}
	setAjaxHeaders(req)

	resp, err := sp.Client.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return false, false, err
	}

	var apiResp OtherDetailsResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return false, false, fmt.Errorf("unmarshal: %w", err)
	}

	return apiResp.Response.Corrigendum, apiResp.Response.Representation, nil
}

// FetchCorrigendumHTML calls POST /bidding/bid/viewCorrigendum/{bid_id}
// Returns the raw HTML response body.
func FetchCorrigendumHTML(sp *SessionPair, bidID int) (string, error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/bidding/bid/viewCorrigendum/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	setAjaxHeaders(req)
	req.Header.Set("Accept", "text/html, */*; q=0.01")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// FetchRepresentationHTML calls GET /publish-representations/{bid_id}
// Returns the raw HTML response body.
func FetchRepresentationHTML(sp *SessionPair, bidID int) (string, error) {
	reqURL := fmt.Sprintf("%s/publish-representations/%d", baseURL, bidID)
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", baseURL+"/all-bids")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// setAjaxHeaders sets common headers for GEM AJAX requests.
func setAjaxHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/all-bids")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
}

// readResponseBody reads the response body, handling gzip if needed.
func readResponseBody(resp *http.Response) ([]byte, error) {
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

	return io.ReadAll(reader)
}

// ScrapeCorrigendums checks all active bids for corrigendum/representation updates.
func ScrapeCorrigendums(pool *SessionPool, db *sql.DB, workers int, rps int, errLog *ErrorLog) error {
	bidIDs, err := GetActiveBidIDs(db)
	if err != nil {
		return fmt.Errorf("get active bids: %w", err)
	}

	if len(bidIDs) == 0 {
		log.Println("[corrigendum] No active bids to check")
		return nil
	}

	log.Printf("[corrigendum] Checking %d active bids (%d workers, %d req/s)", len(bidIDs), workers, rps)

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
		total   = int64(len(bidIDs))
	)

	for w := 0; w < workers; w++ {
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
				done := atomic.LoadInt64(&checked)
				if done%500 == 0 {
					log.Printf("[corrigendum] Progress: %d/%d checked, %d updated, %d errors",
						done, total, atomic.LoadInt64(&updated), atomic.LoadInt64(&errors))
				}
			}
		}()
	}

	wg.Wait()
	log.Printf("[corrigendum] Done: %d checked, %d updated, %d errors",
		checked, updated, errors)
	return nil
}

func processOneBid(sp *SessionPair, db *sql.DB, bidID int, limiter *rate.Limiter) (changed bool, err error) {
	// Step 1: Check flags
	hasCorr, hasRepr, err := FetchOtherDetails(sp, bidID)
	if err != nil {
		return false, fmt.Errorf("bid %d other-details: %w", bidID, err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	// Load existing record (may not exist)
	existing, _ := GetBidOtherDetails(db, bidID)

	details := BidOtherDetails{
		BidID:             bidID,
		HasCorrigendum:    boolToInt(hasCorr),
		HasRepresentation: boolToInt(hasRepr),
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
				if err := InsertCorrigendumDoc(db, doc); err != nil {
					log.Printf("[corrigendum] insert doc error bid=%d corr=%d: %v", bidID, doc.CorrigendumID, err)
				}
			}

			// Update bid end_date if extended
			latestDate := ParseLatestEndDate(html)
			if latestDate != "" {
				details.LatestEndDate = latestDate
				if err := UpdateBidEndDate(db, bidID, latestDate); err != nil {
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
	if err := UpsertBidOtherDetails(db, details); err != nil {
		return false, fmt.Errorf("bid %d upsert: %w", bidID, err)
	}

	return changed, nil
}
