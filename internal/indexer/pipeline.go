package indexer

import (
	"runtime"
	"sort"
	"sync"

	"github.com/bytebase/omni/oracle/ast"
)

type parseJob struct {
	src       *source
	unitIndex int
}

// parseUnits parses independent compilation units in a bounded worker pool.
// Sources and their unit slices are fixed before jobs start, so each worker
// owns a distinct unit. Larger units are started first to reduce the tail.
func (s *state) parseUnits(workers int) {
	var jobs []parseJob
	for _, src := range s.sources {
		for i, u := range src.units {
			if !u.wrapped && !u.limitExceeded {
				jobs = append(jobs, parseJob{src, i})
			}
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i].src.units[jobs[i].unitIndex], jobs[j].src.units[jobs[j].unitIndex]
		return a.end-a.start > b.end-b.start
	})
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers == 0 {
		return
	}
	tasks := make(chan parseJob, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for job := range tasks {
				u := &job.src.units[job.unitIndex]
				list, err := parseRange(job.src.processed, u.start, u.end)
				u.err = err
				if err != nil {
					u.header = parseHeader(job.src.processed, u.start, u.end)
					u.fallback = retainedFallback(job.src, u)
					continue
				}
				if list != nil {
					for _, item := range list.Items {
						if raw, ok := item.(*ast.RawStmt); ok {
							u.node = raw.Stmt
							break
						}
					}
				}
			}
		}()
	}
	for _, job := range jobs {
		tasks <- job
	}
	close(tasks)
	wg.Wait()
}
