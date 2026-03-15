//go:build linux

package firecracker

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sahilshubham/bhatti/pkg/engine"
)

// These tests require root + Firecracker + kernel + rootfs on the Pi.
// Run: sudo go test -v -count=1 -timeout=120s ./pkg/engine/firecracker/

func skipIfNotRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("must run as root")
	}
}

func testEngine(t *testing.T) *Engine {
	t.Helper()
	skipIfNotRoot(t)
	eng, err := New(Config{
		DataDir:    "/var/lib/bhatti",
		KernelPath: "/var/lib/bhatti/images/vmlinux-arm64",
		BaseRootfs: "/var/lib/bhatti/images/rootfs-base-arm64.ext4",
		FCBinary:   "/usr/local/bin/firecracker",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return eng
}

func testSpec(name string) engine.SandboxSpec {
	return engine.SandboxSpec{Name: name, CPUs: 1, MemoryMB: 512}
}

func TestCreateExecDestroy(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("test-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Logf("created %s (ip=%s)", info.ID, info.IP)
	defer eng.Destroy(ctx, info.ID)

	if info.Status != "running" {
		t.Errorf("status: %q", info.Status)
	}

	// uname
	result, err := eng.Exec(ctx, info.ID, []string{"uname", "-a"})
	if err != nil {
		t.Fatalf("Exec uname: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "aarch64") {
		t.Errorf("uname: exit=%d out=%q", result.ExitCode, result.Stdout)
	}
	t.Logf("uname: %s", strings.TrimSpace(result.Stdout))

	// node
	result, err = eng.Exec(ctx, info.ID, []string{"node", "--version"})
	if err != nil {
		t.Fatalf("Exec node: %v", err)
	}
	if !strings.Contains(result.Stdout, "v22") {
		t.Errorf("node: %q", result.Stdout)
	}

	// list
	list, err := eng.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != info.ID {
		t.Errorf("List: %v err=%v", list, err)
	}

	// destroy
	if err := eng.Destroy(ctx, info.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := eng.Status(ctx, info.ID); err == nil {
		t.Error("expected error after destroy")
	}
}

func TestSnapshotResume(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("snap-test"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Write file
	r, _ := eng.Exec(ctx, info.ID, []string{"sh", "-c", "echo snap-data > /tmp/test && cat /tmp/test"})
	if r.ExitCode != 0 {
		t.Fatalf("write: exit=%d err=%q", r.ExitCode, r.Stderr)
	}
	t.Logf("wrote file: %s", strings.TrimSpace(r.Stdout))

	// Stop
	if err := eng.Stop(ctx, info.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	s, _ := eng.Status(ctx, info.ID)
	if s.Status != "stopped" {
		t.Errorf("status: %q", s.Status)
	}
	t.Log("stopped (snapshot created)")

	// Resume
	if err := eng.Start(ctx, info.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Log("resumed")

	// Give the resumed VM a moment to stabilize
	time.Sleep(1 * time.Second)

	// Verify file persists
	r, err = eng.Exec(ctx, info.ID, []string{"cat", "/tmp/test"})
	if err != nil {
		t.Fatalf("read after resume: %v", err)
	}
	if strings.TrimSpace(r.Stdout) != "snap-data" {
		t.Errorf("file: %q, want 'snap-data'", r.Stdout)
	}
	t.Log("file persists across snapshot/resume ✓")
}

func TestShell(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, testSpec("shell-test"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	term, err := eng.Shell(ctx, info.ID)
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	defer term.Close()

	ch := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				ch <- cp
			}
			if err != nil {
				return
			}
		}
	}()

	time.Sleep(500 * time.Millisecond)
	term.Write([]byte("echo shell-works\n"))

	var out strings.Builder
	timer := time.After(5 * time.Second)
	for {
		select {
		case data := <-ch:
			out.Write(data)
			if strings.Contains(out.String(), "shell-works") {
				t.Log("shell inside VM works ✓")
				term.Write([]byte("exit\n"))
				return
			}
		case <-timer:
			t.Fatalf("timeout, output: %q", out.String())
		}
	}
}
