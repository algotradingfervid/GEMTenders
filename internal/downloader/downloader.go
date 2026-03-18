package downloader

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

	"gemtenders/internal/errlog"
	"gemtenders/internal/models"
	"gemtenders/internal/session"
	"gemtenders/internal/store"
)

// downloadFile is a generic file downloader that takes url + destPath + client + maxRetries.
func downloadFile(client *http.Client, url string, destPath string, maxRetries int) error {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		lastErr = doDownload(client, url, destPath)
		if lastErr == nil {
			return nil
		}
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * 2 * time.Second
			time.Sleep(backoff)
		}
	}
	return fmt.Errorf("failed after %d attempts url=%s: %w", maxRetries, url, lastErr)
}

func doDownload(client *http.Client, url string, destPath string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", session.UserAgent)
	req.Header.Set("Accept", "application/pdf,application/x-pdf,*/*")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	if _, err = io.Copy(out, resp.Body); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// DownloadPDFs downloads bid PDF documents for all pending bids.
func DownloadPDFs(db *sql.DB, downloadDir string, workers int, rps int, maxRetries int, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) error {
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}

	ids, err := store.GetPendingDownloads(db)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}

	if len(ids) == 0 {
		log.Println("No pending PDF downloads")
		return nil
	}

	// Filter out already-downloaded files (check file size > 0)
	var pending []int
	for _, id := range ids {
		destPath := filepath.Join(downloadDir, fmt.Sprintf("GeM-Bidding-%d.pdf", id))
		if info, err := os.Stat(destPath); err == nil && info.Size() > 0 {
			if markErr := store.MarkPDFDownloaded(db, id); markErr != nil {
				log.Printf("[pdf-download] mark-downloaded error bid=%d: %v", id, markErr)
			}
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

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 60 * time.Second}

			for bidIDParent := range jobs {
				limiter.Wait(context.Background())

				pdfURL := fmt.Sprintf("%s/showbidDocument/%d", session.BaseURL, bidIDParent)
				destPath := filepath.Join(downloadDir, fmt.Sprintf("GeM-Bidding-%d.pdf", bidIDParent))

				err := downloadFile(client, pdfURL, destPath, maxRetries)
				if err != nil {
					errLog.Log("pdf-download", bidIDParent, err)
					atomic.AddInt64(&failed, 1)
					continue
				}

				if markErr := store.MarkPDFDownloaded(db, bidIDParent); markErr != nil {
					errLog.Log("pdf-mark-downloaded", bidIDParent, markErr)
				}
				done := atomic.AddInt64(&completed, 1)
				if done%100 == 0 {
					f := atomic.LoadInt64(&failed)
					log.Printf("PDF progress: %d/%d downloaded, %d failed", done, total, f)
					if onProgress != nil {
						onProgress(done, total, f, fmt.Sprintf("Downloaded %d/%d PDFs", done, total))
					}
				}
			}
		}()
	}

	wg.Wait()
	log.Printf("PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}

// DownloadCorrigendumPDFs downloads corrigendum PDF documents.
func DownloadCorrigendumPDFs(db *sql.DB, downloadDir string, workers int, rps int, maxRetries int, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) error {
	corrDir := filepath.Join(downloadDir, "corrigendums")
	if err := os.MkdirAll(corrDir, 0755); err != nil {
		return fmt.Errorf("create corrigendum dir: %w", err)
	}

	pending, err := store.GetPendingCorrigendumDownloads(db)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}

	if len(pending) == 0 {
		log.Println("No pending corrigendum PDF downloads")
		return nil
	}

	// Filter out already-downloaded files (check file size > 0)
	var toDownload []models.CorrigendumDoc
	for _, doc := range pending {
		destPath := filepath.Join(corrDir, fmt.Sprintf("Corrigendum-%d-%d.pdf", doc.CorrigendumID, doc.BidID))
		if info, err := os.Stat(destPath); err == nil && info.Size() > 0 {
			if markErr := store.MarkCorrigendumDownloaded(db, doc.ID); markErr != nil {
				log.Printf("[corrigendum-pdf] mark-downloaded error corr=%d bid=%d: %v", doc.CorrigendumID, doc.BidID, markErr)
			}
			continue
		}
		toDownload = append(toDownload, doc)
	}

	if len(toDownload) == 0 {
		log.Println("All corrigendum PDFs already on disk")
		return nil
	}

	log.Printf("Downloading %d corrigendum PDFs with %d workers at %d req/s", len(toDownload), workers, rps)

	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)

	jobs := make(chan models.CorrigendumDoc, len(toDownload))
	for _, doc := range toDownload {
		jobs <- doc
	}
	close(jobs)

	var (
		wg        sync.WaitGroup
		completed int64
		failed    int64
		total     = int64(len(toDownload))
	)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 60 * time.Second}

			for doc := range jobs {
				limiter.Wait(context.Background())

				pdfURL := session.BaseURL + doc.DownloadURL
				destPath := filepath.Join(corrDir, fmt.Sprintf("Corrigendum-%d-%d.pdf", doc.CorrigendumID, doc.BidID))

				err := downloadFile(client, pdfURL, destPath, maxRetries)
				if err != nil {
					errLog.Log("corrigendum-pdf-download", fmt.Sprintf("corr=%d bid=%d", doc.CorrigendumID, doc.BidID), err)
					atomic.AddInt64(&failed, 1)
					continue
				}

				if markErr := store.MarkCorrigendumDownloaded(db, doc.ID); markErr != nil {
					errLog.Log("corrigendum-pdf-mark-downloaded", fmt.Sprintf("corr=%d bid=%d", doc.CorrigendumID, doc.BidID), markErr)
				}
				done := atomic.AddInt64(&completed, 1)
				if done%100 == 0 {
					log.Printf("Corrigendum PDF progress: %d/%d downloaded, %d failed",
						done, total, atomic.LoadInt64(&failed))
				}
			}
		}()
	}

	wg.Wait()
	log.Printf("Corrigendum PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}

// DownloadPDFsWithProgress wraps DownloadPDFs and DownloadCorrigendumPDFs with progress callbacks.
func DownloadPDFsWithProgress(db *sql.DB, downloadDir string, errLog *errlog.ErrorLog, onProgress models.ProgressFunc) error {
	if onProgress != nil {
		onProgress(0, 0, 0, "Starting bid PDF downloads...")
	}

	cfg := models.DefaultDownloadConfig
	err := DownloadPDFs(db, downloadDir, cfg.Workers, cfg.RPS, cfg.MaxRetries, errLog, onProgress)
	if err != nil {
		if onProgress != nil {
			onProgress(0, 0, 1, fmt.Sprintf("Bid PDF download error: %v", err))
		}
		return err
	}
	if onProgress != nil {
		onProgress(0, 0, 0, "Bid PDF downloads completed, starting corrigendum PDF downloads...")
	}

	err = DownloadCorrigendumPDFs(db, downloadDir, cfg.Workers, cfg.RPS, cfg.MaxRetries, errLog, onProgress)
	if err != nil {
		if onProgress != nil {
			onProgress(0, 0, 1, fmt.Sprintf("Corrigendum PDF download error: %v", err))
		}
		return err
	}
	if onProgress != nil {
		onProgress(0, 0, 0, "All PDF downloads completed")
	}
	return nil
}
