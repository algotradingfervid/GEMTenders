package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// ScrapeTask identifies a pipeline stage.
type ScrapeTask string

const (
	TaskScrape      ScrapeTask = "scrape"
	TaskDownload    ScrapeTask = "download"
	TaskCorrigendum ScrapeTask = "corrigendum"
)

// ScrapeProgress is the real-time status snapshot sent to SSE listeners.
type ScrapeProgress struct {
	Task       ScrapeTask `json:"task"`
	Status     string     `json:"status"` // "running", "completed", "error"
	Message    string     `json:"message"`
	Current    int64      `json:"current"`
	Total      int64      `json:"total"`
	Errors     int64      `json:"errors"`
	StartedAt  time.Time  `json:"started_at"`
	ElapsedSec float64    `json:"elapsed_sec"`
}

// JSON serialises the progress for SSE data lines.
func (p ScrapeProgress) JSON() string {
	p.ElapsedSec = time.Since(p.StartedAt).Seconds()
	b, _ := json.Marshal(p)
	return string(b)
}

// ScrapeManager coordinates background scrape runs and fans progress out to SSE listeners.
type ScrapeManager struct {
	mu        sync.RWMutex
	running   bool
	tasks     []ScrapeTask
	progress  ScrapeProgress
	listeners map[chan ScrapeProgress]struct{}
	cancel    context.CancelFunc
	lastRun   *ScrapeProgress
}

// NewScrapeManager returns an initialised manager.
func NewScrapeManager() *ScrapeManager {
	return &ScrapeManager{
		listeners: make(map[chan ScrapeProgress]struct{}),
	}
}

// IsRunning reports whether a scrape is in progress.
func (sm *ScrapeManager) IsRunning() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.running
}

// GetProgress returns a snapshot of the current (or most recent) progress.
func (sm *ScrapeManager) GetProgress() ScrapeProgress {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	p := sm.progress
	if sm.running && !p.StartedAt.IsZero() {
		p.ElapsedSec = time.Since(p.StartedAt).Seconds()
	}
	return p
}

// GetLastRun returns the final progress of the last completed run, or nil.
func (sm *ScrapeManager) GetLastRun() *ScrapeProgress {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastRun
}

// Subscribe returns a buffered channel that receives progress updates.
func (sm *ScrapeManager) Subscribe() chan ScrapeProgress {
	ch := make(chan ScrapeProgress, 16)
	sm.mu.Lock()
	sm.listeners[ch] = struct{}{}
	sm.mu.Unlock()
	return ch
}

// Unsubscribe removes a listener and closes its channel.
func (sm *ScrapeManager) Unsubscribe(ch chan ScrapeProgress) {
	sm.mu.Lock()
	delete(sm.listeners, ch)
	sm.mu.Unlock()
	close(ch)
}

// broadcast sends a progress snapshot to every listener.
// Drops the message for any slow consumer whose buffer is full.
func (sm *ScrapeManager) broadcast(p ScrapeProgress) {
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
func (sm *ScrapeManager) Start(dbPath string, tasks []ScrapeTask, sessionCount int) error {
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks specified")
	}

	sm.mu.Lock()
	if sm.running {
		sm.mu.Unlock()
		return fmt.Errorf("scrape already running")
	}
	sm.running = true
	sm.tasks = tasks
	ctx, cancel := context.WithCancel(context.Background())
	sm.cancel = cancel
	sm.mu.Unlock()

	go sm.run(ctx, dbPath, tasks, sessionCount)
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
func (sm *ScrapeManager) run(ctx context.Context, dbPath string, tasks []ScrapeTask, sessionCount int) {
	startTime := time.Now()
	defer func() {
		sm.mu.Lock()
		sm.running = false
		sm.cancel = nil
		sm.mu.Unlock()
	}()

	// Open a dedicated DB connection for this run
	db, err := InitDB(dbPath)
	if err != nil {
		p := ScrapeProgress{Task: tasks[0], Status: "error", Message: fmt.Sprintf("DB init failed: %v", err), StartedAt: startTime}
		sm.broadcast(p)
		sm.mu.Lock()
		sm.lastRun = &p
		sm.mu.Unlock()
		return
	}
	defer db.Close()

	// Only bootstrap sessions if scrape or corrigendum tasks are requested
	// (downloads don't need CSRF tokens or authenticated sessions)
	var pool *SessionPool
	needsSessions := false
	for _, t := range tasks {
		if t == TaskScrape || t == TaskCorrigendum {
			needsSessions = true
			break
		}
	}
	if needsSessions {
		sm.broadcast(ScrapeProgress{Task: tasks[0], Status: "running", Message: "Bootstrapping sessions...", StartedAt: startTime})
		pool, err = BootstrapSessions(sessionCount)
		if err != nil {
			p := ScrapeProgress{Task: tasks[0], Status: "error", Message: fmt.Sprintf("Session bootstrap failed: %v", err), StartedAt: startTime}
			sm.broadcast(p)
			sm.mu.Lock()
			sm.lastRun = &p
			sm.mu.Unlock()
			return
		}
	}

	errLog := NewErrorLog("web-scrape")
	defer errLog.Close()

	for _, task := range tasks {
		if ctx.Err() != nil {
			p := ScrapeProgress{Task: task, Status: "error", Message: "Cancelled", StartedAt: startTime}
			sm.broadcast(p)
			sm.mu.Lock()
			sm.lastRun = &p
			sm.mu.Unlock()
			return
		}

		sm.broadcast(ScrapeProgress{
			Task:      task,
			Status:    "running",
			Message:   fmt.Sprintf("Starting %s...", task),
			StartedAt: startTime,
		})

		onProgress := func(current, total, errors int64, msg string) {
			sm.broadcast(ScrapeProgress{
				Task:      task,
				Status:    "running",
				Message:   msg,
				Current:   current,
				Total:     total,
				Errors:    errors,
				StartedAt: startTime,
			})
		}

		var taskErr error
		switch task {
		case TaskScrape:
			taskErr = ScrapeBidsWithProgress(pool, db, errLog, onProgress)
		case TaskDownload:
			taskErr = DownloadPDFsWithProgress(db, "downloads", errLog, onProgress)
		case TaskCorrigendum:
			taskErr = ScrapeCorrigenumsWithProgress(pool, db, errLog, onProgress)
		default:
			log.Printf("[scrape-manager] unknown task: %s", task)
		}

		if taskErr != nil {
			sm.broadcast(ScrapeProgress{
				Task:      task,
				Status:    "error",
				Message:   taskErr.Error(),
				StartedAt: startTime,
			})
		} else {
			sm.broadcast(ScrapeProgress{
				Task:      task,
				Status:    "running",
				Message:   fmt.Sprintf("%s finished", task),
				StartedAt: startTime,
			})
		}
	}

	final := ScrapeProgress{
		Task:      tasks[len(tasks)-1],
		Status:    "completed",
		Message:   "All tasks completed",
		StartedAt: startTime,
	}
	sm.broadcast(final)
	sm.mu.Lock()
	sm.lastRun = &final
	sm.mu.Unlock()
}
