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

func ScrapeBids(session *BrowserSession, db *sql.DB, delayMs int) error {
	startPage := GetLastScrapedPage(db) + 1
	log.Printf("Resuming from page %d", startPage)

	// First request to get total count
	totalFound, err := scrapePage(session, db, startPage)
	if err != nil {
		return fmt.Errorf("page %d: %w", startPage, err)
	}

	totalPages := (totalFound + 9) / 10 // ceil division, 10 per page
	log.Printf("Total records: %d, Total pages: %d", totalFound, totalPages)

	for page := startPage + 1; page <= totalPages; page++ {
		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		_, err := scrapePage(session, db, page)
		if err != nil {
			log.Printf("Error on page %d: %v (retrying in 5s)", page, err)
			time.Sleep(5 * time.Second)
			_, err = scrapePage(session, db, page)
			if err != nil {
				log.Printf("Retry failed on page %d: %v (skipping)", page, err)
				continue
			}
		}

		if page%100 == 0 {
			total, downloaded, _ := GetBidCount(db)
			log.Printf("Progress: page %d/%d, %d bids scraped, %d PDFs downloaded", page, totalPages, total, downloaded)
		}
	}

	total, _, _ := GetBidCount(db)
	log.Printf("Scraping complete. Total bids in DB: %d", total)
	return nil
}

func scrapePage(session *BrowserSession, db *sql.DB, page int) (int, error) {
	apiResp, err := session.FetchBidsPage(page)
	if err != nil {
		return 0, fmt.Errorf("fetch page: %w", err)
	}

	if apiResp.Code != 200 {
		return 0, fmt.Errorf("api error: %s", apiResp.Message)
	}

	docs := apiResp.Response.Response.Docs
	if len(docs) > 0 {
		inserted, err := InsertBidsBatch(db, docs)
		if err != nil {
			return 0, fmt.Errorf("insert: %w", err)
		}
		if inserted > 0 {
			log.Printf("Page %d: inserted %d new bids", page, inserted)
		}
	}

	return apiResp.Response.Response.NumFound, nil
}

// DefaultPayloadJSON returns the JSON string for a default payload (used by tests)
func DefaultPayloadJSON(page int) string {
	p := DefaultPayload(page)
	b, _ := json.Marshal(p)
	return string(b)
}
