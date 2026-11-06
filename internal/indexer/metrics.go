package indexer

import (
	"runtime"
	"sync/atomic"
	"time"
)

// Metrics contains optional measurements for the current Index invocation.
// PeakHeapBytes is sampled periodically and can miss shorter allocation peaks.
type Metrics struct {
	Files, Lines, Units, ParsedUnits, DegradedUnits int
	ResolvedReferences                              int
	ParseDuration, Elapsed                          time.Duration
	PeakHeapBytes                                   uint64
}

type heapSampler struct {
	stop chan struct{}
	done chan struct{}
	peak atomic.Uint64
}

func startHeapSampler() *heapSampler {
	s := &heapSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var memory runtime.MemStats
				runtime.ReadMemStats(&memory)
				for old := s.peak.Load(); memory.HeapAlloc > old && !s.peak.CompareAndSwap(old, memory.HeapAlloc); old = s.peak.Load() {
				}
			case <-s.stop:
				var memory runtime.MemStats
				runtime.ReadMemStats(&memory)
				for old := s.peak.Load(); memory.HeapAlloc > old && !s.peak.CompareAndSwap(old, memory.HeapAlloc); old = s.peak.Load() {
				}
				return
			}
		}
	}()
	return s
}

func (s *heapSampler) close() uint64 {
	close(s.stop)
	<-s.done
	return s.peak.Load()
}
