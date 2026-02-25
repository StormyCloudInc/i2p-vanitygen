package generator

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-i2p/i2p-vanitygen/internal/destination"
)

// Result holds a successfully found vanity destination.
type Result struct {
	Destination *destination.Destination
	Address     string
	Attempts    uint64
	Duration    time.Duration
}

// Stats holds progress information for the search.
type Stats struct {
	Checked    uint64
	KeysPerSec float64
	Elapsed    time.Duration
}

// Generator coordinates parallel vanity address searching.
type Generator struct {
	prefix   string
	numCores int
	cancel   context.CancelFunc
	mu       sync.Mutex
}

// New creates a new vanity generator.
func New(prefix string, numCores int) *Generator {
	return &Generator{
		prefix:   strings.ToLower(prefix),
		numCores: numCores,
	}
}

// Start begins the parallel vanity search. Returns channels for results and stats.
func (g *Generator) Start(ctx context.Context) (<-chan Result, <-chan Stats) {
	ctx, cancel := context.WithCancel(ctx)
	g.mu.Lock()
	g.cancel = cancel
	g.mu.Unlock()

	resultCh := make(chan Result, 1)
	statsCh := make(chan Stats, 1)

	var totalChecked atomic.Uint64
	var found atomic.Bool
	startTime := time.Now()

	var workerWg sync.WaitGroup
	var statsWg sync.WaitGroup

	// Launch worker goroutines
	for i := 0; i < g.numCores; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			g.worker(ctx, workerID, &totalChecked, &found, resultCh)
		}(i)
	}

	// Stats reporter
	statsWg.Add(1)
	go func() {
		defer statsWg.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checked := totalChecked.Load()
				elapsed := time.Since(startTime)
				kps := 0.0
				if elapsed.Seconds() > 0 {
					kps = float64(checked) / elapsed.Seconds()
				}
				select {
				case statsCh <- Stats{
					Checked:    checked,
					KeysPerSec: kps,
					Elapsed:    elapsed,
				}:
				default:
				}
			}
		}
	}()

	// Cleanup: wait for workers, cancel context, wait for stats, then close channels
	go func() {
		workerWg.Wait()
		cancel()
		statsWg.Wait()
		close(resultCh)
		close(statsCh)
	}()

	return resultCh, statsCh
}

// Stop cancels the running search.
func (g *Generator) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
	}
}

func (g *Generator) worker(ctx context.Context, workerID int, totalChecked *atomic.Uint64, found *atomic.Bool, resultCh chan<- Result) {
	dest, err := destination.NewRandom()
	if err != nil {
		return
	}

	baseCounter := uint64(workerID) << 48
	counter := baseCounter
	startTime := time.Now()
	batchSize := uint64(1024)

	for {
		if found.Load() {
			return
		}
		// Check context every batchSize iterations to reduce overhead
		if (counter-baseCounter)%batchSize == 0 {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}

		dest.MutateEncryptionKey(counter)
		addr := dest.B32Address()

		counter++
		totalChecked.Add(1)

		if strings.HasPrefix(addr, g.prefix) {
			if found.CompareAndSwap(false, true) {
				resultCh <- Result{
					Destination: dest,
					Address:     dest.FullB32Address(),
					Attempts:    totalChecked.Load(),
					Duration:    time.Since(startTime),
				}
			}
			return
		}
	}
}
