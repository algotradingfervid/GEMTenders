package main

import (
	"context"
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

	"golang.org/x/time/rate"
)

func DownloadPDFs(db *sql.DB, downloadDir string, workers int, rps int, maxRetries int) error {
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

	// Filter out already-downloaded files
	var pending []int
	for _, id := range ids {
		destPath := filepath.Join(downloadDir, fmt.Sprintf("GeM-Bidding-%d.pdf", id))
		if _, err := os.Stat(destPath); err == nil {
			MarkPDFDownloaded(db, id)
			continue
		}
		pending = append(pending, id)
	}

	if len(pending) == 0 {
		log.Println("All PDFs already on disk")
		return nil
	}

	log.Printf("Downloading %d PDFs with %d workers at %d req/s", len(pending), workers, rps)

	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)

	jobs := make(chan int, len(pending))
	for _, id := range pending {
		jobs <- id
	}
	close(jobs)

	var (
		wg        sync.WaitGroup
		completed int64
		failed    int64
		total     = int64(len(pending))
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := &http.Client{Timeout: 60 * time.Second}

			for bidIDParent := range jobs {
				limiter.Wait(context.Background())

				err := downloadWithRetry(client, bidIDParent, downloadDir, maxRetries)
				if err != nil {
					log.Printf("[W%d] Download failed for %d: %v", workerID, bidIDParent, err)
					atomic.AddInt64(&failed, 1)
					continue
				}

				MarkPDFDownloaded(db, bidIDParent)
				done := atomic.AddInt64(&completed, 1)
				if done%100 == 0 {
					log.Printf("PDF progress: %d/%d downloaded, %d failed",
						done, total, atomic.LoadInt64(&failed))
				}
			}
		}(w)
	}

	wg.Wait()
	log.Printf("PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}

func downloadWithRetry(client *http.Client, bidIDParent int, downloadDir string, maxRetries int) error {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		lastErr = downloadPDF(client, bidIDParent, downloadDir)
		if lastErr == nil {
			return nil
		}
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * 2 * time.Second
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

func downloadPDF(client *http.Client, bidIDParent int, downloadDir string) error {
	pdfURL := fmt.Sprintf("%s/showbidDocument/%d", baseURL, bidIDParent)

	req, err := http.NewRequest("GET", pdfURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/pdf,application/x-pdf,*/*")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	filename := fmt.Sprintf("GeM-Bidding-%d.pdf", bidIDParent)
	destPath := filepath.Join(downloadDir, filename)

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(destPath)
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}
