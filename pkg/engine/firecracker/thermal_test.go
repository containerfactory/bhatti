//go:build linux

package firecracker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sahilshubham/bhatti/pkg/engine"
)

func TestPauseResume(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("pause-resume"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Verify running
	r, _ := execWithTimeout(t, eng, info.ID, []string{"echo", "hot"})
	if !strings.Contains(r.Stdout, "hot") {
		t.Fatalf("pre-pause exec failed: %q", r.Stdout)
	}
	if eng.ThermalState(info.ID) != "hot" {
		t.Fatalf("expected hot, got %s", eng.ThermalState(info.ID))
	}

	// Pause (hot → warm)
	start := time.Now()
	if err := eng.Pause(ctx, info.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	pauseLatency := time.Since(start)
	t.Logf("pause latency: %v", pauseLatency)

	if eng.ThermalState(info.ID) != "warm" {
		t.Fatalf("expected warm, got %s", eng.ThermalState(info.ID))
	}

	// Resume (warm → hot)
	start = time.Now()
	if err := eng.Resume(ctx, info.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumeLatency := time.Since(start)
	t.Logf("resume latency: %v", resumeLatency)

	if eng.ThermalState(info.ID) != "hot" {
		t.Fatalf("expected hot after resume, got %s", eng.ThermalState(info.ID))
	}

	// Verify exec still works after resume
	r, err = execWithTimeout(t, eng, info.ID, []string{"echo", "resumed"})
	if err != nil {
		t.Fatalf("post-resume exec: %v", err)
	}
	if !strings.Contains(r.Stdout, "resumed") {
		t.Errorf("post-resume: %q", r.Stdout)
	}
	t.Log("✓ pause/resume cycle works")

	if pauseLatency > 500*time.Millisecond {
		t.Errorf("pause too slow: %v (want <500ms)", pauseLatency)
	}
	if resumeLatency > 500*time.Millisecond {
		t.Errorf("resume too slow: %v (want <500ms)", resumeLatency)
	}
}

func TestPauseResumeMultipleCycles(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("multi-pause"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Write state before cycling
	execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "echo state > /tmp/persist"})

	for i := 0; i < 3; i++ {
		if err := eng.Pause(ctx, info.ID); err != nil {
			t.Fatalf("Pause cycle %d: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond) // let FC API socket settle
		if err := eng.Resume(ctx, info.ID); err != nil {
			t.Fatalf("Resume cycle %d: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// State should persist
	r, _ := execWithTimeout(t, eng, info.ID, []string{"cat", "/tmp/persist"})
	if strings.TrimSpace(r.Stdout) != "state" {
		t.Errorf("state lost after 3 pause/resume cycles: %q", r.Stdout)
	} else {
		t.Log("✓ state persists across 3 pause/resume cycles")
	}
}

func TestEnsureHotFromWarm(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("ensure-warm"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	eng.Pause(ctx, info.ID)
	if eng.ThermalState(info.ID) != "warm" {
		t.Fatalf("expected warm")
	}

	start := time.Now()
	if err := eng.EnsureHot(ctx, info.ID); err != nil {
		t.Fatalf("EnsureHot from warm: %v", err)
	}
	latency := time.Since(start)
	t.Logf("ensureHot(warm→hot) latency: %v", latency)

	if eng.ThermalState(info.ID) != "hot" {
		t.Fatalf("expected hot")
	}

	r, _ := execWithTimeout(t, eng, info.ID, []string{"echo", "warmed"})
	if !strings.Contains(r.Stdout, "warmed") {
		t.Errorf("exec after ensureHot: %q", r.Stdout)
	}
	t.Log("✓ ensureHot from warm works")
}

func TestEnsureHotFromCold(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("ensure-cold"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Write data, then go cold
	execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "echo cold-data > /tmp/data"})
	eng.Stop(ctx, info.ID)
	if eng.ThermalState(info.ID) != "cold" {
		t.Fatalf("expected cold")
	}

	start := time.Now()
	if err := eng.EnsureHot(ctx, info.ID); err != nil {
		t.Fatalf("EnsureHot from cold: %v", err)
	}
	latency := time.Since(start)
	t.Logf("ensureHot(cold→hot) latency: %v", latency)

	if eng.ThermalState(info.ID) != "hot" {
		t.Fatalf("expected hot")
	}

	r, _ := execWithTimeout(t, eng, info.ID, []string{"cat", "/tmp/data"})
	if strings.TrimSpace(r.Stdout) != "cold-data" {
		t.Errorf("data lost after cold→hot: %q", r.Stdout)
	}
	t.Log("✓ ensureHot from cold works, data preserved")
}

func TestEnsureHotAlreadyHot(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("ensure-hot"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	start := time.Now()
	if err := eng.EnsureHot(ctx, info.ID); err != nil {
		t.Fatalf("EnsureHot when already hot: %v", err)
	}
	latency := time.Since(start)

	if latency > 10*time.Millisecond {
		t.Errorf("ensureHot(hot) should be instant, took %v", latency)
	}
	t.Logf("✓ ensureHot(hot) is no-op (%v)", latency)
}

func TestActivityTracking(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("activity"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Query activity before any exec
	activity, err := eng.Activity(ctx, info.ID)
	if err != nil {
		t.Fatalf("Activity: %v", err)
	}
	t.Logf("initial activity: last=%d active=%d attached=%d",
		activity.LastActivityUnix, activity.ActiveSessions, activity.AttachedSessions)

	// Exec something to update activity
	beforeExec := time.Now().Unix()
	execWithTimeout(t, eng, info.ID, []string{"echo", "bump"})
	time.Sleep(100 * time.Millisecond)

	activity2, err := eng.Activity(ctx, info.ID)
	if err != nil {
		t.Fatalf("Activity after exec: %v", err)
	}

	// Timestamp should be recent (within last 5 seconds)
	now := time.Now().Unix()
	if activity2.LastActivityUnix < beforeExec || activity2.LastActivityUnix > now {
		t.Errorf("activity timestamp out of range: got %d, expected between %d and %d",
			activity2.LastActivityUnix, beforeExec, now)
	} else {
		t.Logf("✓ activity timestamp is recent: %ds ago", now-activity2.LastActivityUnix)
	}
	t.Logf("after exec: last=%d active=%d attached=%d",
		activity2.LastActivityUnix, activity2.ActiveSessions, activity2.AttachedSessions)

	// Create a TTY session (attached)
	vm, _ := eng.getVM(info.ID)
	_, term, _ := vm.Agent.ShellSession(ctx, []string{"sleep", "3600"}, nil, 24, 80)
	defer term.Close()
	time.Sleep(300 * time.Millisecond)

	activity3, _ := eng.Activity(ctx, info.ID)
	if activity3.ActiveSessions < 1 {
		t.Errorf("expected >= 1 active session, got %d", activity3.ActiveSessions)
	}
	if activity3.AttachedSessions < 1 {
		t.Errorf("expected >= 1 attached session, got %d", activity3.AttachedSessions)
	}
	t.Logf("with TTY session: active=%d attached=%d",
		activity3.ActiveSessions, activity3.AttachedSessions)

	// Detach
	term.Close()
	time.Sleep(300 * time.Millisecond)

	activity4, _ := eng.Activity(ctx, info.ID)
	if activity4.AttachedSessions != 0 {
		t.Errorf("expected 0 attached after detach, got %d", activity4.AttachedSessions)
	}
	t.Logf("after detach: active=%d attached=%d",
		activity4.ActiveSessions, activity4.AttachedSessions)
	t.Log("✓ activity tracking works")
}

func TestAttachedSessionPreventsWarm(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("prevent-warm"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Create attached session
	vm, _ := eng.getVM(info.ID)
	_, term, _ := vm.Agent.ShellSession(ctx, []string{"sleep", "3600"}, nil, 24, 80)
	defer term.Close()
	time.Sleep(300 * time.Millisecond)

	// Query activity — should show attached session
	activity, _ := eng.Activity(ctx, info.ID)
	if activity.AttachedSessions == 0 {
		t.Fatal("expected attached session")
	}

	// In production, the thermal manager would check AttachedSessions > 0
	// and skip pausing. We verify the data is correct.
	t.Logf("✓ attached=%d — thermal manager would skip pause", activity.AttachedSessions)
}

func TestExecOnWarmVMFails(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("exec-warm"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	eng.Pause(ctx, info.ID)

	start := time.Now()
	_, err = eng.Exec(ctx, info.ID, []string{"echo", "hello"})
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected error exec on warm VM")
	} else {
		t.Logf("✓ exec on warm VM rejected in %v: %v", elapsed, err)
	}
	if elapsed > 1*time.Second {
		t.Errorf("rejection too slow: %v (want <1s)", elapsed)
	}

	// Resume and verify it works
	eng.Resume(ctx, info.ID)
	r, err := execWithTimeout(t, eng, info.ID, []string{"echo", "back"})
	if err != nil || !strings.Contains(r.Stdout, "back") {
		t.Fatalf("exec after resume: err=%v out=%q", err, r.Stdout)
	}
	t.Log("✓ exec works after resume")
}

func TestExecOnColdVMFails(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("exec-cold"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	eng.Stop(ctx, info.ID)

	start := time.Now()
	_, err = eng.Exec(ctx, info.ID, []string{"echo", "hello"})
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected error exec on cold VM")
	} else {
		t.Logf("✓ exec on cold VM rejected in %v: %v", elapsed, err)
	}
	if elapsed > 1*time.Second {
		t.Errorf("rejection too slow: %v (want <1s)", elapsed)
	}
}

// --- Performance benchmarks ---

func TestPerfVMBootTime(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	start := time.Now()
	info, err := eng.Create(ctx, testSpec("perf-boot"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	bootTime := time.Since(start)
	defer eng.Destroy(ctx, info.ID)

	t.Logf("⏱ VM boot time (create → agent ready): %v", bootTime)
	if bootTime > 5*time.Second {
		t.Errorf("boot too slow: %v (want <5s)", bootTime)
	}
}

func TestPerfExecLatency(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-exec"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Warm up
	execWithTimeout(t, eng, info.ID, []string{"true"})

	// Measure 10 exec roundtrips
	var total time.Duration
	const N = 10
	for i := 0; i < N; i++ {
		start := time.Now()
		r, err := execWithTimeout(t, eng, info.ID, []string{"echo", "perf"})
		elapsed := time.Since(start)
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("exec %d: err=%v exit=%d", i, err, r.ExitCode)
		}
		total += elapsed
	}
	avg := total / N
	t.Logf("⏱ exec latency (avg of %d): %v", N, avg)
	if avg > 500*time.Millisecond {
		t.Errorf("exec too slow: %v avg (want <500ms)", avg)
	}
}

func TestPerfPauseResumeLatency(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-pause"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	var pauseTotal, resumeTotal, execTotal time.Duration
	const N = 3
	for i := 0; i < N; i++ {
		start := time.Now()
		eng.Pause(ctx, info.ID)
		pauseTotal += time.Since(start)

		time.Sleep(50 * time.Millisecond)

		start = time.Now()
		eng.Resume(ctx, info.ID)
		resumeTotal += time.Since(start)

		// Measure exec immediately after resume
		start = time.Now()
		execWithTimeout(t, eng, info.ID, []string{"true"})
		execTotal += time.Since(start)

		time.Sleep(50 * time.Millisecond)
	}

	t.Logf("⏱ pause latency  (avg of %d): %v", N, pauseTotal/N)
	t.Logf("⏱ resume latency (avg of %d): %v", N, resumeTotal/N)
	t.Logf("⏱ exec-after-resume (avg of %d): %v", N, execTotal/N)
}

func TestPerfSnapshotRestoreLatency(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-snap"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Write some state so memory isn't trivially empty
	execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "dd if=/dev/urandom of=/tmp/state bs=1M count=10 2>/dev/null"})

	start := time.Now()
	eng.Stop(ctx, info.ID)
	snapLatency := time.Since(start)

	// Report snapshot file sizes
	vm, _ := eng.getVM(info.ID)
	if memInfo, err := os.Stat(vm.SnapMemPath); err == nil {
		t.Logf("⏱ snapshot mem file: %dMB", memInfo.Size()/(1024*1024))
	}
	if vmInfo, err := os.Stat(vm.SnapVMPath); err == nil {
		t.Logf("⏱ snapshot vm file:  %dKB", vmInfo.Size()/1024)
	}

	start = time.Now()
	eng.Start(ctx, info.ID)
	restoreLatency := time.Since(start)

	// Verify it works
	r, _ := execWithTimeout(t, eng, info.ID, []string{"ls", "/tmp/state"})
	if !strings.Contains(r.Stdout, "state") {
		t.Error("state file missing after restore")
	}

	t.Logf("⏱ snapshot latency: %v", snapLatency)
	t.Logf("⏱ restore latency:  %v", restoreLatency)
}

func TestPerfExecThroughput(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-throughput"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Warm up
	execWithTimeout(t, eng, info.ID, []string{"true"})

	// Measure how many execs per second
	const duration = 3 * time.Second
	start := time.Now()
	count := 0
	for time.Since(start) < duration {
		r, err := execWithTimeout(t, eng, info.ID, []string{"true"})
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("exec %d failed: %v", count, err)
		}
		count++
	}
	elapsed := time.Since(start)
	throughput := float64(count) / elapsed.Seconds()
	t.Logf("⏱ exec throughput: %.1f execs/sec (%d in %v)", throughput, count, elapsed)
}

func TestPerfConcurrentExec(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-concurrent"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Warm up
	execWithTimeout(t, eng, info.ID, []string{"true"})

	// 10 concurrent execs
	const N = 10
	type result struct {
		elapsed time.Duration
		err     error
	}
	results := make(chan result, N)

	start := time.Now()
	for i := 0; i < N; i++ {
		go func(i int) {
			s := time.Now()
			_, err := execWithTimeout(t, eng, info.ID, []string{"echo", "concurrent"})
			results <- result{time.Since(s), err}
		}(i)
	}

	var maxLatency time.Duration
	var errors int
	for i := 0; i < N; i++ {
		r := <-results
		if r.err != nil {
			errors++
		}
		if r.elapsed > maxLatency {
			maxLatency = r.elapsed
		}
	}
	totalElapsed := time.Since(start)

	t.Logf("⏱ %d concurrent execs: total=%v max_latency=%v errors=%d",
		N, totalElapsed, maxLatency, errors)
	if errors > 0 {
		t.Errorf("%d/%d concurrent execs failed", errors, N)
	}
}

func TestPerfMultiVMBoot(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	const N = 3
	start := time.Now()
	var infos []engine.SandboxInfo
	for i := 0; i < N; i++ {
		info, err := eng.Create(ctx, testSpec(fmt.Sprintf("perf-multi-%d", i)))
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		infos = append(infos, info)
	}
	totalBoot := time.Since(start)
	defer func() {
		for _, info := range infos {
			eng.Destroy(ctx, info.ID)
		}
	}()

	t.Logf("⏱ %d VMs sequential boot: %v (avg %v/VM)", N, totalBoot, totalBoot/N)

	// Verify all work
	for _, info := range infos {
		r, _ := execWithTimeout(t, eng, info.ID, []string{"true"})
		if r.ExitCode != 0 {
			t.Errorf("VM %s exec failed", info.ID)
		}
	}
}

func TestPerfEnsureHotWarm(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("perf-ensure"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Pause then measure ensureHot
	eng.Pause(ctx, info.ID)

	start := time.Now()
	eng.EnsureHot(ctx, info.ID)
	warmToHot := time.Since(start)

	// Exec to verify
	start = time.Now()
	execWithTimeout(t, eng, info.ID, []string{"true"})
	execAfter := time.Since(start)

	t.Logf("⏱ ensureHot(warm→hot):     %v", warmToHot)
	t.Logf("⏱ first exec after resume: %v", execAfter)
	t.Logf("⏱ total warm→exec:         %v", warmToHot+execAfter)
}
