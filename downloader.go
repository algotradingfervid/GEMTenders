package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

func DownloadPDFs(session *BrowserSession, db *sql.DB, downloadDir string, workers int, delayMs int) error {
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

	// Filter out IDs that already have files on disk
	var pending []int
	for _, id := range ids {
		destPath := filepath.Join(downloadDir, fmt.Sprintf("GeM-Bidding-%d.pdf", id))
		if _, err := os.Stat(destPath); err == nil {
			// File exists, mark as downloaded in DB
			MarkPDFDownloaded(db, id)
			continue
		}
		pending = append(pending, id)
	}

	if len(pending) == 0 {
		log.Println("All PDFs already downloaded on disk")
		return nil
	}

	log.Printf("Downloading %d PDFs in batches of %d", len(pending), workers)

	var completed, failed int
	total := len(pending)

	for batchStart := 0; batchStart < total; batchStart += workers {
		batchEnd := batchStart + workers
		if batchEnd > total {
			batchEnd = total
		}
		batch := pending[batchStart:batchEnd]

		if delayMs > 0 && batchStart > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		results, err := session.DownloadPDFBatch(batch)
		if err != nil {
			log.Printf("Batch %d-%d failed: %v (retrying individually)", batchStart, batchEnd, err)
			for _, id := range batch {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
				data, err := session.DownloadPDFBytes(id)
				if err != nil {
					log.Printf("Download failed for %d: %v", id, err)
					failed++
					continue
				}
				destPath := filepath.Join(downloadDir, fmt.Sprintf("GeM-Bidding-%d.pdf", id))
				if err := os.WriteFile(destPath, data, 0644); err != nil {
					log.Printf("Write failed for %d: %v", id, err)
					failed++
					continue
				}
				MarkPDFDownloaded(db, id)
				completed++
			}
			continue
		}

		// Write successful downloads to disk
		for _, id := range batch {
			data, ok := results[id]
			if !ok || data == nil {
				failed++
				continue
			}
			destPath := filepath.Join(downloadDir, fmt.Sprintf("GeM-Bidding-%d.pdf", id))
			if err := os.WriteFile(destPath, data, 0644); err != nil {
				log.Printf("Write failed for %d: %v", id, err)
				failed++
				continue
			}
			MarkPDFDownloaded(db, id)
			completed++
		}

		log.Printf("PDF progress: %d/%d downloaded, %d failed (batch %d-%d)",
			completed, total, failed, batchStart+1, batchEnd)
	}

	log.Printf("PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}
