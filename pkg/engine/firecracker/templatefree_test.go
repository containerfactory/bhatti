//go:build linux

package firecracker

import (
	"context"
	"strings"
	"testing"

	"github.com/sahilshubham/bhatti/pkg/engine"
)

func TestCreateWithoutTemplate(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, engine.SandboxSpec{
		Name:     "direct-create",
		CPUs:     1,
		MemoryMB: 512,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	if info.Status != "running" {
		t.Errorf("status: %q", info.Status)
	}

	r, _ := execWithTimeout(t, eng, info.ID, []string{"echo", "hello direct"})
	if strings.TrimSpace(r.Stdout) != "hello direct" {
		t.Errorf("exec: %q", r.Stdout)
	}
	t.Log("✓ direct creation works")
}

func TestCreateWithoutTemplateDefaults(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	// Minimal spec — engine should apply defaults
	info, err := eng.Create(ctx, engine.SandboxSpec{
		Name: "defaults-test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	r, _ := execWithTimeout(t, eng, info.ID, []string{"nproc"})
	if strings.TrimSpace(r.Stdout) != "1" {
		t.Errorf("nproc: %q, want 1 (default)", r.Stdout)
	}

	r, _ = execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "free -m | awk '/Mem:/{print $2}'"})
	t.Logf("memory: %s MB", strings.TrimSpace(r.Stdout))
	t.Log("✓ default CPU/memory applied")
}

func TestCreateWithEnvAndInit(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, engine.SandboxSpec{
		Name:     "env-init-test",
		CPUs:     1,
		MemoryMB: 512,
		Env: map[string]string{
			"MY_VAR": "hello-env",
		},
		Init: "echo init-ran > /tmp/init-marker",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Verify env
	r, _ := execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "echo $MY_VAR"})
	if strings.TrimSpace(r.Stdout) != "hello-env" {
		t.Errorf("env: %q, want hello-env", r.Stdout)
	}

	// Wait for init to complete
	r, _ = execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "for i in $(seq 1 20); do [ -f /tmp/init-marker ] && break; sleep 0.5; done; cat /tmp/init-marker"})
	if strings.TrimSpace(r.Stdout) != "init-ran" {
		t.Errorf("init marker: %q", r.Stdout)
	}
	t.Log("✓ env vars and init script work")
}

func TestCreateWithVolume(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	info, err := eng.Create(ctx, engine.SandboxSpec{
		Name:     "vol-test",
		CPUs:     1,
		MemoryMB: 512,
		NewVolumes: []engine.VolumeSpec{
			{Name: "data", SizeMB: 64, Mount: "/data"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer eng.Destroy(ctx, info.ID)

	// Verify mount exists and is writable
	r, _ := execWithTimeout(t, eng, info.ID, []string{"sh", "-c", "echo vol-data > /data/test.txt && cat /data/test.txt"})
	if strings.TrimSpace(r.Stdout) != "vol-data" {
		t.Errorf("volume: %q", r.Stdout)
	}

	// Verify it's a real mount (not just a directory on rootfs)
	r, _ = execWithTimeout(t, eng, info.ID, []string{"mountpoint", "-q", "/data"})
	if r.ExitCode != 0 {
		t.Errorf("/data is not a mountpoint")
	}
	t.Log("✓ new volume created and mounted")
}
