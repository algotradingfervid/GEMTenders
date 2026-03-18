package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Config controls worker pool behavior.
type Config struct {
	Workers  int
	RPS      int
	Interval time.Duration // progress report interval
}

// Progress holds the current state of a worker pool run.
type Progress struct {
	Done, Total, Errors int64
}

// Run executes jobs across a pool of workers with rate limiting and progress reporting.
func Run[T any](jobs []T, cfg Config, process func(T) error, onProgress func(Progress, string)) error {
	if len(jobs) == 0 {
		return nil
	}

	limiter := rate.NewLimiter(rate.Limit(cfg.RPS), cfg.RPS*2)

	ch := make(chan T, len(jobs))
	for _, j := range jobs {
		ch <- j
	}
	close(ch)

	total := int64(len(jobs))
	var done, errors int64

	interval := cfg.Interval
	if interval == 0 {
		interval = 3 * time.Second
	}

	// Progress reporter goroutine
	stopProgress := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopProgress:
				return
			case <-ticker.C:
				d := atomic.LoadInt64(&done)
				e := atomic.LoadInt64(&errors)
				if onProgress != nil {
					onProgress(Progress{Done: d, Total: total, Errors: e}, "")
				}
			}
		}
	}()

	var wg sync.WaitGroup
	for range cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range ch {
				limiter.Wait(context.Background())
				if err := process(job); err != nil {
					atomic.AddInt64(&errors, 1)
				}
				atomic.AddInt64(&done, 1)
			}
		}()
	}

	wg.Wait()
	close(stopProgress)

	// Final progress report
	if onProgress != nil {
		onProgress(Progress{Done: done, Total: total, Errors: errors}, "complete")
	}

	return nil
}
