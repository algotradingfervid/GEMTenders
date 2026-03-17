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
	case "serve":
		runServeCmd(os.Args[2:])
	case "reindex":
		runReindexCmd(os.Args[2:])
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
  status     Show scraping/download progress
  serve      Start the web server for tender discovery
  reindex    Rebuild the full-text search index`)
}

func runScrapeCmd(args []string) {
	fs := flag.NewFlagSet("scrape", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	sessions := fs.Int("sessions", 3, "Number of sessions to bootstrap")
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

	log.Println("=== Checking corrigendums/representations ===")
	if err := ScrapeCorrigendums(pool, db, *workers, *rps); err != nil {
		log.Printf("Corrigendum check error: %v", err)
		// Don't exit — bid scraping succeeded
	}

	total, downloaded, _ := GetBidCount(db)
	log.Printf("=== Done === Total bids: %d, PDFs downloaded: %d", total, downloaded)
}

func runDownloadCmd(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	downloadDir := fs.String("dir", "downloads", "PDF download directory")
	workers := fs.Int("workers", 100, "Number of download goroutines")
	rps := fs.Int("rps", 50, "Download requests per second")
	retries := fs.Int("retries", 5, "Max retry attempts per download")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	log.Println("=== Downloading bid PDFs ===")
	if err := DownloadPDFs(db, *downloadDir, *workers, *rps, *retries); err != nil {
		log.Printf("Download error: %v", err)
		os.Exit(1)
	}

	log.Println("=== Downloading corrigendum PDFs ===")
	if err := DownloadCorrigendumPDFs(db, *downloadDir, *workers, *rps, *retries); err != nil {
		log.Printf("Corrigendum download error: %v", err)
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

	checked, withCorr, docsTotal, docsDownloaded, _ := GetCorrigendumStats(db)
	fmt.Printf("Corrigendums checked: %d\n", checked)
	fmt.Printf("Bids with corrigendums: %d\n", withCorr)
	fmt.Printf("Corrigendum PDFs:     %d downloaded / %d total\n", docsDownloaded, docsTotal)
}

func runReindexCmd(args []string) {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	if err := InitFTS(db); err != nil {
		log.Fatalf("Failed to init FTS: %v", err)
	}
	if err := RebuildFTS(db); err != nil {
		log.Fatalf("Failed to rebuild FTS: %v", err)
	}

	total, downloaded, _ := GetBidCount(db)
	log.Printf("Reindex complete. Total bids: %d, PDFs: %d", total, downloaded)
}

func runServeCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	downloadDir := fs.String("downloads", "downloads", "PDF download directory")
	addr := fs.String("addr", ":28080", "Server listen address")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	StartServer(db, *downloadDir, *addr)
}
