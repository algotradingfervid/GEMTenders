package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	dbPath := flag.String("db", "gems.db", "SQLite database path")
	downloadDir := flag.String("downloads", "downloads", "PDF download directory")
	concurrency := flag.Int("concurrency", 3, "Number of concurrent PDF downloads")
	delayMs := flag.Int("delay", 500, "Delay between API requests in milliseconds")
	skipScrape := flag.Bool("skip-scrape", false, "Skip scraping, only download PDFs")
	skipDownload := flag.Bool("skip-download", false, "Skip PDF downloads, only scrape listings")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Initialize database
	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	// Bootstrap browser session (keeps browser alive for all requests)
	session, err := NewBrowserSession()
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	// Phase 1: Scrape bid listings
	if !*skipScrape {
		log.Println("=== Phase 1: Scraping bid listings ===")
		if err := ScrapeBids(session, db, *delayMs); err != nil {
			log.Printf("Scraping error: %v", err)
			os.Exit(1)
		}
	}

	// Phase 2: Download PDFs
	if !*skipDownload {
		log.Println("=== Phase 2: Downloading bid PDFs ===")
		if err := DownloadPDFs(session, db, *downloadDir, *concurrency, *delayMs); err != nil {
			log.Printf("Download error: %v", err)
			os.Exit(1)
		}
	}

	// Print summary
	total, downloaded, _ := GetBidCount(db)
	log.Printf("=== Done === Total bids: %d, PDFs downloaded: %d", total, downloaded)
}
