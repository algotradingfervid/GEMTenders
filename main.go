package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	dbPath := flag.String("db", "gems.db", "SQLite database path")
	downloadDir := flag.String("downloads", "downloads", "PDF download directory")
	sessions := flag.Int("sessions", 10, "Number of sessions to bootstrap")
	scrapeWorkers := flag.Int("scrape-workers", 20, "Number of scraping goroutines")
	downloadWorkers := flag.Int("download-workers", 10, "Number of PDF download goroutines")
	scrapeRPS := flag.Int("scrape-rps", 20, "Scraping requests per second (global)")
	downloadRPS := flag.Int("download-rps", 10, "Download requests per second (global)")
	skipScrape := flag.Bool("skip-scrape", false, "Skip scraping, only download PDFs")
	skipDownload := flag.Bool("skip-download", false, "Skip PDF downloads, only scrape listings")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	pool, err := BootstrapSessions(*sessions)
	if err != nil {
		log.Fatalf("Failed to bootstrap sessions: %v", err)
	}

	if !*skipScrape {
		log.Println("=== Phase 1: Scraping bid listings ===")
		if err := ScrapeBids(pool, db, *scrapeWorkers, *scrapeRPS); err != nil {
			log.Printf("Scraping error: %v", err)
			os.Exit(1)
		}
	}

	if !*skipDownload {
		log.Println("=== Phase 2: Downloading bid PDFs ===")
		if err := DownloadPDFs(pool, db, *downloadDir, *downloadWorkers, *downloadRPS); err != nil {
			log.Printf("Download error: %v", err)
			os.Exit(1)
		}
	}

	total, downloaded, _ := GetBidCount(db)
	log.Printf("=== Done === Total bids: %d, PDFs downloaded: %d", total, downloaded)
}
