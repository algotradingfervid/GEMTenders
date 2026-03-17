package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

func DownloadPDFs(session *BrowserSession, db *sql.DB, downloadDir string, concurrency int, delayMs int) error {
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}

	ids, err := GetPendingDownloads(db)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}

	if len(ids) == 0 {
		log.Println("No pending PDF downloads")
		return nil
	}

	log.Printf("Downloading %d PDFs (sequential via browser, delay=%dms)", len(ids), delayMs)

	// Downloads must be sequential since they go through a single browser context
	// The concurrency parameter is kept for API compatibility but not used here
	var completed, failed int64
	total := len(ids)

	for _, bidIDParent := range ids {
		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		destPath := filepath.Join(downloadDir, fmt.Sprintf("GeM-Bidding-%d.pdf", bidIDParent))

		// Skip if already exists on disk
		if _, err := os.Stat(destPath); err == nil {
			if err := MarkPDFDownloaded(db, bidIDParent); err != nil {
				log.Printf("Failed to mark %d as downloaded: %v", bidIDParent, err)
			}
			atomic.AddInt64(&completed, 1)
			continue
		}

		data, err := session.DownloadPDFBytes(bidIDParent)
		if err != nil {
			log.Printf("Download failed for %d: %v", bidIDParent, err)
			atomic.AddInt64(&failed, 1)
			continue
		}

		if err := os.WriteFile(destPath, data, 0644); err != nil {
			log.Printf("Write failed for %d: %v", bidIDParent, err)
			atomic.AddInt64(&failed, 1)
			continue
		}

		if err := MarkPDFDownloaded(db, bidIDParent); err != nil {
			log.Printf("Failed to mark %d as downloaded: %v", bidIDParent, err)
		}

		completed++
		if completed%100 == 0 {
			log.Printf("PDF progress: %d/%d downloaded, %d failed", completed, total, failed)
		}
	}

	log.Printf("PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}
