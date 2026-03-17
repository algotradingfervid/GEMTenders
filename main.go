package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	switch os.Args[1] {
	case "scrape":
		runScrapeCmd(os.Args[2:])
	case "download":
		runDownloadCmd(os.Args[2:])
	case "status":
		runStatusCmd(os.Args[2:])
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: gemscraper <command> [flags]

Commands:
  scrape     Scrape bid listings from GEM portal
  download   Download PDF documents for scraped bids
  status     Show scraping/download progress`)
}

func runScrapeCmd(args []string) {
	fs := flag.NewFlagSet("scrape", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	sessions := fs.Int("sessions", 10, "Number of sessions to bootstrap")
	scrapers := fs.Int("scrapers", 5, "Number of parallel scraper instances")
	staggerSec := fs.Int("stagger", 30, "Seconds between scraper launches")
	workers := fs.Int("workers", 20, "Workers per scraper instance")
	rps := fs.Int("rps", 20, "Requests per second per scraper")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	pool, err := BootstrapSessions(*sessions)
	if err != nil {
		log.Fatalf("Failed to bootstrap sessions: %v", err)
	}

	log.Println("=== Scraping bid listings ===")
	if err := ScrapeBids(pool, db, *scrapers, *staggerSec, *workers, *rps); err != nil {
		log.Printf("Scraping error: %v", err)
		os.Exit(1)
	}

	total, downloaded, _ := GetBidCount(db)
	log.Printf("=== Done === Total bids: %d, PDFs downloaded: %d", total, downloaded)
}

func runDownloadCmd(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	downloadDir := fs.String("dir", "downloads", "PDF download directory")
	sessions := fs.Int("sessions", 10, "Number of sessions to bootstrap")
	workers := fs.Int("workers", 10, "Number of download goroutines")
	rps := fs.Int("rps", 10, "Download requests per second")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	pool, err := BootstrapSessions(*sessions)
	if err != nil {
		log.Fatalf("Failed to bootstrap sessions: %v", err)
	}

	log.Println("=== Downloading bid PDFs ===")
	if err := DownloadPDFs(pool, db, *downloadDir, *workers, *rps); err != nil {
		log.Printf("Download error: %v", err)
		os.Exit(1)
	}

	total, downloaded, _ := GetBidCount(db)
	log.Printf("=== Done === Total bids: %d, PDFs downloaded: %d", total, downloaded)
}

func runStatusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	total, downloaded, _ := GetBidCount(db)
	pending, _ := GetPendingDownloads(db)
	fmt.Printf("Total bids:        %d\n", total)
	fmt.Printf("PDFs downloaded:   %d\n", downloaded)
	fmt.Printf("PDFs pending:      %d\n", len(pending))
}
