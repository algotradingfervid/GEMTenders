package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

func DefaultPayload(page int) APIPayload {
	return APIPayload{
		Page: page,
		Param: APIParam{
			SearchBid:  "",
			SearchType: "fullText",
		},
		Filter: APIFilter{
			BidStatusType: "ongoing_bids",
			ByType:        "all",
			HighBidValue:  "",
			ByEndDate:     APIDateRange{From: "", To: ""},
			Sort:          "Bid-End-Date-Oldest",
		},
	}
}

func ScrapeBids(session *BrowserSession, db *sql.DB, workers int, delayMs int) error {
	startPage := GetLastScrapedPage(db) + 1

	// First single request to get total count
	log.Printf("Fetching page %d to get total count...", startPage)
	apiResp, err := session.FetchBidsPage(startPage)
	if err != nil {
		return fmt.Errorf("page %d: %w", startPage, err)
	}
	if apiResp.Code != 200 {
		return fmt.Errorf("api error: %s", apiResp.Message)
	}

	// Insert first page results
	docs := apiResp.Response.Response.Docs
	if len(docs) > 0 {
		inserted, _ := InsertBidsBatch(db, docs)
		if inserted > 0 {
			log.Printf("Page %d: inserted %d new bids", startPage, inserted)
		}
	}

	totalFound := apiResp.Response.Response.NumFound
	totalPages := (totalFound + 9) / 10
	log.Printf("Total records: %d, Total pages: %d, Workers: %d", totalFound, totalPages, workers)

	// Process remaining pages in batches
	for batchStart := startPage + 1; batchStart <= totalPages; batchStart += workers {
		batchEnd := batchStart + workers - 1
		if batchEnd > totalPages {
			batchEnd = totalPages
		}

		// Build page list for this batch
		var pages []int
		for p := batchStart; p <= batchEnd; p++ {
			pages = append(pages, p)
		}

		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		results, err := session.FetchBidsPagesBatch(pages)
		if err != nil {
			log.Printf("Batch %d-%d failed: %v (retrying individually)", batchStart, batchEnd, err)
			// Fallback: try pages individually
			for _, p := range pages {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
				resp, err := session.FetchBidsPage(p)
				if err != nil {
					log.Printf("Page %d failed: %v (skipping)", p, err)
					continue
				}
				if resp.Code == 200 && len(resp.Response.Response.Docs) > 0 {
					InsertBidsBatch(db, resp.Response.Response.Docs)
				}
			}
			continue
		}

		// Parse and insert each page's results
		totalInserted := 0
		for i, resultStr := range results {
			if resultStr == "" {
				log.Printf("Page %d: empty result (skipped)", pages[i])
				continue
			}

			var resp APIResponse
			if err := json.Unmarshal([]byte(resultStr), &resp); err != nil {
				log.Printf("Page %d: parse error: %v", pages[i], err)
				continue
			}
			if resp.Code != 200 || len(resp.Response.Response.Docs) == 0 {
				continue
			}
			inserted, err := InsertBidsBatch(db, resp.Response.Response.Docs)
			if err != nil {
				log.Printf("Page %d: insert error: %v", pages[i], err)
				continue
			}
			totalInserted += inserted
		}

		if totalInserted > 0 {
			log.Printf("Batch %d-%d: inserted %d new bids", batchStart, batchEnd, totalInserted)
		}

		// Progress log every 10 batches
		if (batchStart-startPage)/(workers) % 10 == 0 {
			total, downloaded, _ := GetBidCount(db)
			log.Printf("Progress: pages %d-%d/%d, %d bids scraped, %d PDFs downloaded",
				batchStart, batchEnd, totalPages, total, downloaded)
		}
	}

	total, _, _ := GetBidCount(db)
	log.Printf("Scraping complete. Total bids in DB: %d", total)
	return nil
}
