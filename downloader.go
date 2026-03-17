package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

func DownloadPDFs(session *Session, db *sql.DB, downloadDir string, concurrency int, delayMs int) error {
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

	log.Printf("Downloading %d PDFs with concurrency=%d", len(ids), concurrency)

	var (
		wg        sync.WaitGroup
		sem       = make(chan struct{}, concurrency)
		completed int64
		failed    int64
		total     = len(ids)
	)

	for _, bidIDParent := range ids {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore

		go func(id int) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore

			if delayMs > 0 {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
			}

			err := downloadPDF(session, id, downloadDir)
			if err != nil {
				log.Printf("Download failed for %d: %v", id, err)
				atomic.AddInt64(&failed, 1)
				return
			}

			if err := MarkPDFDownloaded(db, id); err != nil {
				log.Printf("Failed to mark %d as downloaded: %v", id, err)
			}

			done := atomic.AddInt64(&completed, 1)
			if done%100 == 0 {
				log.Printf("PDF progress: %d/%d downloaded, %d failed", done, total, atomic.LoadInt64(&failed))
			}
		}(bidIDParent)
	}

	wg.Wait()
	log.Printf("PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}

func downloadPDF(session *Session, bidIDParent int, downloadDir string) error {
	url := fmt.Sprintf("%s/showbidDocument/%d", baseURL, bidIDParent)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Referer", baseURL+"/all-bids")

	for _, cookie := range session.Cookies {
		req.AddCookie(cookie)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	// Use suggested filename from Content-Disposition, fallback to bid ID
	filename := fmt.Sprintf("GeM-Bidding-%d.pdf", bidIDParent)

	destPath := filepath.Join(downloadDir, filename)

	// Skip if already exists on disk
	if _, err := os.Stat(destPath); err == nil {
		return nil
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(destPath) // clean up partial file
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}
