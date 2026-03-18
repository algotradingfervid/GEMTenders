package errlog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrorLog provides a thread-safe error logger that writes to a file.
// All subsystems (scraper, corrigendum, downloader) share one instance per run.
type ErrorLog struct {
	file   *os.File
	logger *log.Logger
	mu     sync.Mutex
	count  int
	path   string
}

// NewErrorLog creates a timestamped error log file in the logs/ directory.
// Returns a no-op logger if file creation fails (logs warning to stderr).
func NewErrorLog(prefix string) *ErrorLog {
	os.MkdirAll("logs", 0755)
	path := filepath.Join("logs", fmt.Sprintf("%s_errors_%s.log", prefix, time.Now().Format("2006-01-02_15-04-05")))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Warning: could not create error log %s: %v", path, err)
		return &ErrorLog{path: path}
	}
	log.Printf("Error log: %s", path)
	return &ErrorLog{
		file:   file,
		logger: log.New(file, "", log.LstdFlags),
		path:   path,
	}
}

// Log writes an error entry to both stderr and the log file.
func (e *ErrorLog) Log(category string, id any, err error) {
	msg := fmt.Sprintf("[%s] id=%v error=%v", category, id, err)
	log.Printf("%s", msg)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.count++
	if e.logger != nil {
		e.logger.Printf("%s", msg)
	}
}

// Count returns the number of errors logged.
func (e *ErrorLog) Count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

// Close closes the log file and reports summary.
func (e *ErrorLog) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.count > 0 {
		log.Printf("Total errors: %d — see %s", e.count, e.path)
	}
	if e.file != nil {
		e.file.Close()
	}
}
