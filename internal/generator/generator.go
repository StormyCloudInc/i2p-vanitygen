package generator

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-i2p/i2p-vanitygen/internal/address"
	"github.com/go-i2p/i2p-vanitygen/internal/gpu"
)

// Result holds a successfully found vanity address.
type Result struct {
	Candidate address.Candidate
	Address   string
	Attempts  uint64
	Duration  time.Duration
}

// Stats holds progress information for the search.
type Stats struct {
	Checked    uint64
	KeysPerSec float64
	Elapsed    time.Duration
}

// Generator coordinates parallel vanity address searching.
type Generator struct {
	scheme    address.Scheme
	prefixes  []string
	numCores  int
	useGPU    bool
	gpuDevice int
	cancel    context.CancelFunc
	mu        sync.Mutex
}

// New creates a new vanity generator.
func New(scheme address.Scheme, prefixes []string, numCores int, useGPU bool, gpuDevice int) *Generator {
	lower := make([]string, len(prefixes))
	for i, p := range prefixes {
		lower[i] = strings.ToLower(p)
	}
	return &Generator{
		scheme:    scheme,
		prefixes:  lower,
		numCores:  numCores,
		useGPU:    useGPU,
		gpuDevice: gpuDevice,
	}
}

// shortestPrefix returns the shortest prefix from the set (used for GPU filtering).
func (g *Generator) shortestPrefix() string {
	if len(g.prefixes) == 0 {
		return ""
	}
	shortest := g.prefixes[0]
	for _, p := range g.prefixes[1:] {
		if len(p) < len(shortest) {
			shortest = p
		}
	}
	return shortest
}

// matchesAny checks if addr starts with any of the configured prefixes.
func (g *Generator) matchesAny(addr string) bool {
	for _, p := range g.prefixes {
		if strings.HasPrefix(addr, p) {
			return true
		}
	}
	return false
}

// sendResult attempts to send a result without blocking. Returns true if sent.
func sendResult(ctx context.Context, resultCh chan<- Result, r Result) bool {
	select {
	case resultCh <- r:
		return true
	case <-ctx.Done():
		return false
	}
}

// Start begins the parallel vanity search. Returns channels for results and stats.
// The generator runs until the context is canceled — it does NOT stop on finding
// a match. The caller decides when to cancel (after first result, or never for
// endless mode).
func (g *Generator) Start(ctx context.Context) (<-chan Result, <-chan Stats) {
	ctx, cancel := context.WithCancel(ctx)
	g.mu.Lock()
	g.cancel = cancel
	g.mu.Unlock()

	resultCh := make(chan Result, 16)
	statsCh := make(chan Stats, 1)

	var totalChecked atomic.Uint64
	startTime := time.Now()

	var workerWg sync.WaitGroup
	var statsWg sync.WaitGroup

	// Launch GPU worker if enabled and scheme supports it
	cpuWorkerOffset := 0
	if g.useGPU && g.scheme.SupportsGPU() && gpu.Available() {
		cpuWorkerOffset = 1 // reserve workerID 0 counter space for GPU
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			switch g.scheme.Network() {
			case address.NetworkI2P:
				g.gpuWorker(ctx, &totalChecked, resultCh, startTime)
			case address.NetworkTorV3:
				g.torV3GPUWorker(ctx, &totalChecked, resultCh, startTime)
			}
		}()
	}

	// Launch CPU worker goroutines
	for i := 0; i < g.numCores; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			g.worker(ctx, workerID+cpuWorkerOffset, &totalChecked, resultCh)
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

func (g *Generator) gpuWorker(ctx context.Context, totalChecked *atomic.Uint64, resultCh chan<- Result, startTime time.Time) {
	// GPU only works with I2P scheme (needs the raw destination template)
	cand, err := g.scheme.NewCandidate()
	if err != nil {
		return
	}
	i2pCand, ok := cand.(*address.I2PCandidate)
	if !ok {
		return // GPU not supported for this scheme
	}

	batchSize := uint64(1 << 22) // ~4M hashes per dispatch
	gpuW, err := gpu.NewWorker(gpu.WorkerConfig{
		DeviceIndex:  g.gpuDevice,
		DestTemplate: i2pCand.Raw(),
		Prefix:       g.shortestPrefix(),
		BatchSize:    batchSize,
	})
	if err != nil {
		return // GPU unavailable, CPU workers continue
	}
	defer gpuW.Close()

	counter := uint64(0) // GPU uses workerID 0 counter space

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		result, err := gpuW.RunBatch(counter)
		if err != nil {
			return // GPU error, stop GPU worker
		}

		totalChecked.Add(result.Checked)
		counter += result.Checked

		if result.Found {
			// GPU matched shortest prefix; verify against all prefixes on CPU
			i2pCand.Dest.MutateEncryptionKey(result.MatchCounter)
			addr := i2pCand.FullAddress()
			if !g.matchesAny(addr) {
				continue // false positive from shorter GPU prefix
			}
			sendResult(ctx, resultCh, Result{
				Candidate: i2pCand,
				Address:   addr,
				Attempts:  totalChecked.Load(),
				Duration:  time.Since(startTime),
			})
			// Generate a fresh candidate for next matches (the current one was mutated)
			newCand, err := g.scheme.NewCandidate()
			if err != nil {
				return
			}
			i2pCand = newCand.(*address.I2PCandidate)
		}
	}
}

func (g *Generator) torV3GPUWorker(ctx context.Context, totalChecked *atomic.Uint64, resultCh chan<- Result, startTime time.Time) {
	cand, err := address.NewTorV3Candidate()
	if err != nil {
		return
	}

	batchSize := uint64(1 << 18) // 262144 keys per GPU dispatch
	gpuW, err := gpu.NewTorV3Worker(gpu.TorV3WorkerConfig{
		DeviceIndex: g.gpuDevice,
		Prefix:      g.shortestPrefix(),
		BatchSize:   batchSize,
	})
	if err != nil {
		return // GPU unavailable, CPU workers continue
	}
	defer gpuW.Close()

	// Use multiple CPU cores to precompute keys in parallel.
	// Each core handles a chunk of the batch starting at a different offset.
	precomputeCores := runtime.NumCPU()
	if precomputeCores > 8 {
		precomputeCores = 8 // diminishing returns beyond 8
	}
	if precomputeCores < 1 {
		precomputeCores = 1
	}
	chunkSize := batchSize / uint64(precomputeCores)

	// Double-buffer pipeline: CPU precomputes into one buffer while GPU
	// processes the other, eliminating idle time on both sides.
	bufA := make([]byte, batchSize*32)
	bufB := make([]byte, batchSize*32)

	type gpuBatchResult struct {
		result gpu.BatchResult
		err    error
	}
	gpuDone := make(chan gpuBatchResult, 1)

	// parallelPrecompute fills buf with pubkeys using multiple goroutines.
	// cand is advanced by batchSize keys total. Returns the snapshot from
	// before precomputation started.
	parallelPrecompute := func(buf []byte) *address.TorV3Candidate {
		snapshot := cand.Clone()

		if precomputeCores == 1 {
			// Fast path: no goroutine overhead
			for i := uint64(0); i < batchSize; i++ {
				copy(buf[i*32:(i+1)*32], cand.PublicKeyBytes())
				cand.Advance()
			}
			return snapshot
		}

		// Create per-chunk candidates at the right offsets
		chunks := make([]*address.TorV3Candidate, precomputeCores)
		chunks[0] = cand.Clone() // chunk 0 starts at current position
		for c := 1; c < precomputeCores; c++ {
			chunks[c] = cand.Clone()
			chunks[c].AdvanceBy(uint64(c) * chunkSize)
		}

		// Parallel precompute
		var wg sync.WaitGroup
		for c := 0; c < precomputeCores; c++ {
			wg.Add(1)
			go func(chunkIdx int) {
				defer wg.Done()
				cc := chunks[chunkIdx]
				start := uint64(chunkIdx) * chunkSize
				end := start + chunkSize
				if chunkIdx == precomputeCores-1 {
					end = batchSize // last chunk gets any remainder
				}
				for i := start; i < end; i++ {
					copy(buf[i*32:(i+1)*32], cc.PublicKeyBytes())
					cc.Advance()
				}
			}(c)
		}
		wg.Wait()

		// Advance cand past this entire batch
		cand.AdvanceBy(batchSize)
		return snapshot
	}

	// Precompute first batch into bufA
	snapshotA := parallelPrecompute(bufA)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Submit current batch to GPU (non-blocking via goroutine)
		currentBuf := bufA
		currentSnapshot := snapshotA
		go func() {
			r, e := gpuW.RunBatch(currentBuf, batchSize)
			gpuDone <- gpuBatchResult{r, e}
		}()

		// While GPU works, CPU precomputes next batch into the other buffer
		snapshotB := parallelPrecompute(bufB)

		// Wait for GPU result
		gr := <-gpuDone
		if gr.err != nil {
			return
		}

		totalChecked.Add(gr.result.Checked)

		if gr.result.Found {
			// GPU matched shortest prefix; verify against all prefixes on CPU
			currentSnapshot.AdvanceBy(gr.result.MatchCounter)
			addr := currentSnapshot.FullAddress()
			if g.matchesAny(addr) {
				sendResult(ctx, resultCh, Result{
					Candidate: currentSnapshot,
					Address:   addr,
					Attempts:  totalChecked.Load(),
					Duration:  time.Since(startTime),
				})
			}
		}

		// Swap buffers: next iteration submits bufB, precomputes into bufA
		bufA, bufB = bufB, bufA
		snapshotA = snapshotB
	}
}

func (g *Generator) worker(ctx context.Context, workerID int, totalChecked *atomic.Uint64, resultCh chan<- Result) {
	startTime := time.Now()

	switch g.scheme.Network() {
	case address.NetworkI2P:
		g.i2pWorker(ctx, workerID, totalChecked, resultCh, startTime)
	case address.NetworkTorV3:
		g.torV3Worker(ctx, workerID, totalChecked, resultCh, startTime)
	}
}

func (g *Generator) i2pWorker(ctx context.Context, workerID int, totalChecked *atomic.Uint64, resultCh chan<- Result, startTime time.Time) {
	cand, err := g.scheme.NewCandidate()
	if err != nil {
		return
	}
	i2pCand := cand.(*address.I2PCandidate)

	baseCounter := uint64(workerID) << 48
	counter := baseCounter
	batchSize := uint64(1024)
	localChecked := uint64(0)

	for {
		if localChecked%batchSize == 0 {
			totalChecked.Add(localChecked)
			localChecked = 0
			select {
			case <-ctx.Done():
				return
			default:
			}
		}

		if i2pCand.MutateAndCheckAny(counter, g.prefixes) {
			totalChecked.Add(localChecked + 1)
			localChecked = 0
			sendResult(ctx, resultCh, Result{
				Candidate: i2pCand,
				Address:   i2pCand.FullAddress(),
				Attempts:  totalChecked.Load(),
				Duration:  time.Since(startTime),
			})
			// Generate fresh candidate for next match
			newCand, err := g.scheme.NewCandidate()
			if err != nil {
				return
			}
			i2pCand = newCand.(*address.I2PCandidate)
			counter = baseCounter // reset counter for new candidate
			continue
		}

		counter++
		localChecked++
	}
}

func (g *Generator) torV3Worker(ctx context.Context, workerID int, totalChecked *atomic.Uint64, resultCh chan<- Result, startTime time.Time) {
	cand, err := address.NewTorV3Candidate()
	if err != nil {
		return
	}

	// Each worker starts at a different offset to avoid overlap
	if workerID > 0 {
		cand.AdvanceBy(uint64(workerID) << 48)
	}

	batchSize := uint64(1024)
	localChecked := uint64(0)

	for {
		if localChecked%batchSize == 0 {
			totalChecked.Add(localChecked)
			localChecked = 0
			select {
			case <-ctx.Done():
				return
			default:
			}
		}

		matched := false
		for _, p := range g.prefixes {
			if cand.CheckPrefixFast(p) {
				matched = true
				break
			}
		}
		if matched {
			totalChecked.Add(localChecked + 1)
			localChecked = 0
			// Clone before sending — cand continues advancing after this
			snapshot := cand.Clone()
			sendResult(ctx, resultCh, Result{
				Candidate: snapshot,
				Address:   snapshot.FullAddress(),
				Attempts:  totalChecked.Load(),
				Duration:  time.Since(startTime),
			})
		}

		cand.Advance()
		localChecked++
	}
}
