//go:build linux

package firecracker

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sahil-shubham/bhatti/pkg/agent"
	"github.com/sahil-shubham/bhatti/pkg/engine"
)

// --- Percentile helpers ---

type latencies []time.Duration

func (l latencies) p(pct float64) time.Duration {
	if len(l) == 0 {
		return 0
	}
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	idx := int(float64(len(l)) * pct / 100)
	if idx >= len(l) {
		idx = len(l) - 1
	}
	return l[idx]
}

func (l latencies) report(name string, t *testing.T) {
	t.Helper()
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	t.Logf("⏱ %s (n=%d): p50=%v  p95=%v  p99=%v  max=%v",
		name, len(l),
		l.p(50).Round(time.Microsecond),
		l.p(95).Round(time.Microsecond),
		l.p(99).Round(time.Microsecond),
		l[len(l)-1].Round(time.Microsecond))
}

// --- Perf workload tests ---

func TestPerfExecPercentiles(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-exec"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Warmup
	for i := 0; i < 5; i++ {
		execWithTimeout(t, eng, info.ID, []string{"true"})
	}

	// 100 sequential execs
	var lats latencies
	for i := 0; i < 100; i++ {
		start := time.Now()
		r, err := execWithTimeout(t, eng, info.ID, []string{"true"})
		elapsed := time.Since(start)
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("exec %d failed: err=%v exit=%d", i, err, r.ExitCode)
		}
		lats = append(lats, elapsed)
	}
	lats.report("exec `true`", t)

	if lats.p(99) > 50*time.Millisecond {
		t.Errorf("p99 exec latency too high: %v (want <50ms)", lats.p(99))
	}
}

func TestPerfSmallFileLatency(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-file"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	content := []byte(strings.Repeat("x", 1024)) // 1KB

	// 50 write+read cycles
	var writeLats, readLats latencies
	for i := 0; i < 50; i++ {
		path := fmt.Sprintf("/workspace/perf-%d.txt", i)

		start := time.Now()
		err := eng.FileWrite(ctx, info.ID, path, "0644", int64(len(content)), bytes.NewReader(content))
		writeLats = append(writeLats, time.Since(start))
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}

		start = time.Now()
		var buf bytes.Buffer
		_, _, err = eng.FileRead(ctx, info.ID, path, &buf)
		readLats = append(readLats, time.Since(start))
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if buf.Len() != 1024 {
			t.Fatalf("read %d: got %d bytes, want 1024", i, buf.Len())
		}
	}
	writeLats.report("1KB file write", t)
	readLats.report("1KB file read", t)
}

func TestPerfParallelFileReads(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-pread"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Write 5 files
	for i := 0; i < 5; i++ {
		path := fmt.Sprintf("/workspace/file-%d.txt", i)
		content := []byte(fmt.Sprintf("content-%d %s", i, strings.Repeat("x", 1024)))
		eng.FileWrite(ctx, info.ID, path, "0644", int64(len(content)), bytes.NewReader(content))
	}

	// Read all 5 in parallel — common agentic pattern
	var mu sync.Mutex
	var lats latencies
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path := fmt.Sprintf("/workspace/file-%d.txt", idx)
			start := time.Now()
			var buf bytes.Buffer
			_, _, err := eng.FileRead(ctx, info.ID, path, &buf)
			elapsed := time.Since(start)
			if err != nil {
				t.Errorf("parallel read %d: %v", idx, err)
				return
			}
			mu.Lock()
			lats = append(lats, elapsed)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(lats) != 5 {
		t.Fatalf("expected 5 reads, got %d", len(lats))
	}
	lats.report("5 parallel 1KB reads", t)

	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	t.Logf("  slowest single read: %v", lats[len(lats)-1].Round(time.Microsecond))
}

func TestPerfWarmExecHTTPLatency(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-warm"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Pause → warm
	if err := eng.Pause(ctx, info.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	// 5 cycles: ensureHot (warm→hot) + exec + pause
	// Fewer cycles to avoid Firecracker's Unix socket connection limit.
	var lats latencies
	for i := 0; i < 5; i++ {
		start := time.Now()
		if err := eng.EnsureHot(ctx, info.ID); err != nil {
			t.Fatalf("EnsureHot %d: %v", i, err)
		}
		r, err := execWithTimeout(t, eng, info.ID, []string{"true"})
		elapsed := time.Since(start)
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("exec %d: err=%v exit=%d", i, err, r.ExitCode)
		}
		lats = append(lats, elapsed)
		eng.Pause(ctx, info.ID)
		time.Sleep(50 * time.Millisecond) // let FC socket pool drain
	}
	lats.report("warm→exec (resume+exec)", t)
}

func TestPerfDiffVsFullSnapshot(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-diff"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "echo data > /tmp/test"})

	// First stop → Full
	start := time.Now()
	eng.Stop(ctx, info.ID)
	fullDur := time.Since(start)

	vm, _ := eng.getVM(info.ID)
	fullSize, _ := fileSize(vm.SnapMemPath)

	eng.Start(ctx, info.ID)

	// Small write to dirty a few pages
	execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "echo more > /tmp/test2"})

	// Second stop → Diff
	start = time.Now()
	eng.Stop(ctx, info.ID)
	diffDur := time.Since(start)

	diffSize, _ := fileSize(vm.SnapMemPath)

	eng.Start(ctx, info.ID)

	// Verify data integrity
	r, _ := execWithTimeout(t, eng, info.ID, []string{"cat", "/tmp/test"})
	if !strings.Contains(r.Stdout, "data") {
		t.Fatalf("data lost: %q", r.Stdout)
	}
	r, _ = execWithTimeout(t, eng, info.ID, []string{"cat", "/tmp/test2"})
	if !strings.Contains(r.Stdout, "more") {
		t.Fatalf("data2 lost: %q", r.Stdout)
	}

	t.Logf("⏱ full snapshot: %v (%d MB)", fullDur.Round(time.Millisecond), fullSize/(1024*1024))
	t.Logf("⏱ diff snapshot: %v (%d MB)", diffDur.Round(time.Millisecond), diffSize/(1024*1024))
	if diffSize < fullSize {
		t.Logf("✓ diff is %dx smaller", fullSize/diffSize)
	}
}

func TestPerfStreamExecLatency(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-stream"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Measure time-to-first-byte and time-to-exit for streaming exec
	var ttfbLats, totalLats latencies
	for i := 0; i < 20; i++ {
		start := time.Now()
		var firstByte time.Duration
		var gotFirst bool
		eng.ExecStream(ctx, info.ID, []string{"echo", "perf-stream"}, func(e engine.StreamEvent) {
			if !gotFirst && e.Type == "stdout" {
				firstByte = time.Since(start)
				gotFirst = true
			}
		})
		total := time.Since(start)

		if gotFirst {
			ttfbLats = append(ttfbLats, firstByte)
		}
		totalLats = append(totalLats, total)
	}
	ttfbLats.report("stream exec TTFB", t)
	totalLats.report("stream exec total", t)
}

func TestPerfConcurrentExecLatency(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-conc"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Warmup
	execWithTimeout(t, eng, info.ID, []string{"true"})

	// 10 concurrent execs — common in agentic workloads
	// (LLM returns multiple tool calls in one message)
	var mu sync.Mutex
	var lats latencies
	var wg sync.WaitGroup
	errors := 0

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			start := time.Now()
			r, err := execWithTimeout(t, eng, info.ID, []string{"echo", fmt.Sprintf("concurrent-%d", idx)})
			elapsed := time.Since(start)
			mu.Lock()
			defer mu.Unlock()
			if err != nil || r.ExitCode != 0 {
				errors++
				return
			}
			lats = append(lats, elapsed)
		}(i)
	}
	wg.Wait()

	if errors > 0 {
		t.Errorf("%d/%d concurrent execs failed", errors, 10)
	}
	lats.report("10 concurrent execs", t)
	t.Logf("  errors: %d/10", errors)
}

func TestPerfTruncatedFileRead(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-trunc"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Write a 10K-line file
	execWithTimeout(t, eng, info.ID, []string{"sh", "-c",
		"for i in $(seq 1 10000); do echo \"line $i of the log file with some padding to make it realistic\"; done > /workspace/big.log"})

	// Full read
	var fullLats latencies
	for i := 0; i < 10; i++ {
		var buf bytes.Buffer
		start := time.Now()
		eng.FileRead(ctx, info.ID, "/workspace/big.log", &buf)
		fullLats = append(fullLats, time.Since(start))
	}
	fullLats.report("10K-line full read", t)

	// Truncated read (limit=100)
	var truncLats latencies
	for i := 0; i < 10; i++ {
		var buf bytes.Buffer
		start := time.Now()
		eng.FileRead(ctx, info.ID, "/workspace/big.log", &buf,
			agent.FileReadOpts{Limit: 100})
		truncLats = append(truncLats, time.Since(start))
	}
	truncLats.report("10K-line truncated read (limit=100)", t)

	speedup := float64(fullLats.p(50)) / float64(truncLats.p(50))
	t.Logf("  truncation speedup: %.1fx at p50", speedup)
}
