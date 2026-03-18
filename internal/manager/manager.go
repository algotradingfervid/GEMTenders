package manager

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"gemtenders/internal/downloader"
	"gemtenders/internal/errlog"
	"gemtenders/internal/models"
	"gemtenders/internal/scraper"
	"gemtenders/internal/session"
	"gemtenders/internal/store"
)

// ScrapeManager coordinates background scrape runs and fans progress out to SSE listeners.
type ScrapeManager struct {
	mu        sync.RWMutex
	running   bool
	progress  models.ScrapeProgress
	listeners map[chan models.ScrapeProgress]struct{}
	cancel    context.CancelFunc
	lastRun   *models.ScrapeProgress
}

// NewScrapeManager returns an initialised manager.
func NewScrapeManager() *ScrapeManager {
	return &ScrapeManager{
		listeners: make(map[chan models.ScrapeProgress]struct{}),
	}
}

// IsRunning reports whether a scrape is in progress.
func (sm *ScrapeManager) IsRunning() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.running
}

// GetProgress returns a snapshot of the current (or most recent) progress.
func (sm *ScrapeManager) GetProgress() models.ScrapeProgress {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	p := sm.progress
	if sm.running && !p.StartedAt.IsZero() {
		p.ElapsedSec = time.Since(p.StartedAt).Seconds()
	}
	return p
}

// GetLastRun returns the final progress of the last completed run, or nil.
func (sm *ScrapeManager) GetLastRun() *models.ScrapeProgress {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastRun
}

// Subscribe returns a buffered channel that receives progress updates.
func (sm *ScrapeManager) Subscribe() chan models.ScrapeProgress {
	ch := make(chan models.ScrapeProgress, 16)
	sm.mu.Lock()
	sm.listeners[ch] = struct{}{}
	sm.mu.Unlock()
	return ch
}

// Unsubscribe removes a listener and closes its channel.
func (sm *ScrapeManager) Unsubscribe(ch chan models.ScrapeProgress) {
	sm.mu.Lock()
	delete(sm.listeners, ch)
	sm.mu.Unlock()
	close(ch)
}

// broadcast sends a progress snapshot to every listener.
// Drops the message for any slow consumer whose buffer is full.
func (sm *ScrapeManager) broadcast(p models.ScrapeProgress) {
	if !p.StartedAt.IsZero() {
		p.ElapsedSec = time.Since(p.StartedAt).Seconds()
	}

	sm.mu.Lock()
	sm.progress = p
	for ch := range sm.listeners {
		select {
		case ch <- p:
		default:
			// slow consumer — drop
		}
	}
	sm.mu.Unlock()
}

// Start kicks off a background scrape run.
// Returns an error immediately if a run is already in progress.
// dbPath and sessionCount are used to open a fresh DB connection and bootstrap sessions
// inside the goroutine (SQLite connections are not safe to share across goroutines that
// do heavy concurrent writes).
// downloadDir is the directory for PDF downloads.
func (sm *ScrapeManager) Start(dbPath string, tasks []models.ScrapeTask, sessionCount int, downloadDir string) error {
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks specified")
	}

	sm.mu.Lock()
	if sm.running {
		sm.mu.Unlock()
		return fmt.Errorf("scrape already running")
	}
	sm.running = true
	ctx, cancel := context.WithCancel(context.Background())
	sm.cancel = cancel
	sm.mu.Unlock()

	go sm.run(ctx, dbPath, tasks, sessionCount, downloadDir)
	return nil
}

// Stop cancels the running scrape, if any.
func (sm *ScrapeManager) Stop() {
	sm.mu.RLock()
	cancel := sm.cancel
	sm.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// run executes the requested tasks sequentially in the background.
func (sm *ScrapeManager) run(ctx context.Context, dbPath string, tasks []models.ScrapeTask, sessionCount int, downloadDir string) {
	startTime := time.Now()
	defer func() {
		sm.mu.Lock()
		sm.running = false
		sm.cancel = nil
		sm.mu.Unlock()
	}()

	// Open a dedicated DB connection for this run
	db, err := store.InitDB(dbPath)
	if err != nil {
		p := models.ScrapeProgress{Task: tasks[0], Status: models.StatusError, Message: fmt.Sprintf("DB init failed: %v", err), StartedAt: startTime}
		sm.broadcast(p)
		sm.mu.Lock()
		sm.lastRun = &p
		sm.mu.Unlock()
		return
	}
	defer db.Close()

	// Only bootstrap sessions if scrape or corrigendum tasks are requested
	// (downloads don't need CSRF tokens or authenticated sessions)
	var pool *session.SessionPool
	needsSessions := false
	for _, t := range tasks {
		if t == models.TaskScrape || t == models.TaskCorrigendum {
			needsSessions = true
			break
		}
	}
	if needsSessions {
		sm.broadcast(models.ScrapeProgress{Task: tasks[0], Status: models.StatusRunning, Message: "Bootstrapping sessions...", StartedAt: startTime})
		pool, err = session.BootstrapSessions(sessionCount)
		if err != nil {
			p := models.ScrapeProgress{Task: tasks[0], Status: models.StatusError, Message: fmt.Sprintf("Session bootstrap failed: %v", err), StartedAt: startTime}
			sm.broadcast(p)
			sm.mu.Lock()
			sm.lastRun = &p
			sm.mu.Unlock()
			return
		}
	}

	errLog := errlog.NewErrorLog("web-scrape")
	defer errLog.Close()

	for _, task := range tasks {
		if ctx.Err() != nil {
			p := models.ScrapeProgress{Task: task, Status: models.StatusError, Message: "Cancelled", StartedAt: startTime}
			sm.broadcast(p)
			sm.mu.Lock()
			sm.lastRun = &p
			sm.mu.Unlock()
			return
		}

		sm.broadcast(models.ScrapeProgress{
			Task:      task,
			Status:    models.StatusRunning,
			Message:   fmt.Sprintf("Starting %s...", task),
			StartedAt: startTime,
		})

		onProgress := func(current, total, errors int64, msg string) {
			sm.broadcast(models.ScrapeProgress{
				Task:      task,
				Status:    models.StatusRunning,
				Message:   msg,
				Current:   current,
				Total:     total,
				Errors:    errors,
				StartedAt: startTime,
			})
		}

		var taskErr error
		switch task {
		case models.TaskScrape:
			taskErr = scraper.ScrapeBidsWithProgress(pool, db, errLog, onProgress)
		case models.TaskDownload:
			taskErr = downloader.DownloadPDFsWithProgress(db, downloadDir, errLog, onProgress)
		case models.TaskCorrigendum:
			taskErr = scraper.ScrapeCorrigenumsWithProgress(pool, db, errLog, onProgress)
		default:
			log.Printf("[scrape-manager] unknown task: %s", task)
		}

		if taskErr != nil {
			sm.broadcast(models.ScrapeProgress{
				Task:      task,
				Status:    models.StatusError,
				Message:   taskErr.Error(),
				StartedAt: startTime,
			})
		} else {
			sm.broadcast(models.ScrapeProgress{
				Task:      task,
				Status:    models.StatusRunning,
				Message:   fmt.Sprintf("%s finished", task),
				StartedAt: startTime,
			})
		}
	}

	final := models.ScrapeProgress{
		Task:      tasks[len(tasks)-1],
		Status:    models.StatusCompleted,
		Message:   "All tasks completed",
		StartedAt: startTime,
	}
	sm.broadcast(final)
	sm.mu.Lock()
	sm.lastRun = &final
	sm.mu.Unlock()
}
