package main

import (
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// cliTest holds the test server + helpers for CLI integration tests.
// The CLI binary talks to a real bhatti HTTP server backed by a real
// Firecracker engine on the Pi.
type cliTest struct {
	t       *testing.T
	bhatti  string // path to bhatti binary
	baseURL string // daemon URL
	token   string // auth token (empty = no auth)
}

// run executes a bhatti CLI command and returns stdout, stderr, exit code.
func (c *cliTest) run(args ...string) (stdout, stderr string, exitCode int) {
	c.t.Helper()
	cmd := exec.Command(c.bhatti, args...)
	cmd.Env = append(os.Environ(),
		"BHATTI_URL="+c.baseURL,
		"BHATTI_TOKEN="+c.token,
	)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			c.t.Fatalf("run %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// setupCLITest creates a cliTest pointing at the daemon running on localhost:8080.
// The bhatti binary must be at /usr/local/bin/bhatti (installed by the deploy step).
func setupCLITest(t *testing.T) *cliTest {
	t.Helper()

	bhatti := "/usr/local/bin/bhatti"
	if _, err := os.Stat(bhatti); err != nil {
		t.Skipf("bhatti binary not found at %s", bhatti)
	}

	// Verify daemon is running
	resp, err := http.Get("http://localhost:8080/sandboxes")
	if err != nil {
		t.Skipf("bhatti daemon not running: %v", err)
	}
	resp.Body.Close()

	return &cliTest{
		t:       t,
		bhatti:  bhatti,
		baseURL: "http://localhost:8080",
	}
}

// --- Tests ---

func TestCLICreate(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, code := c.run("create", "--name", "cli-test-create")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	// Output: ID\tNAME\tIP
	parts := strings.Fields(stdout)
	if len(parts) < 2 {
		t.Fatalf("unexpected output: %q", stdout)
	}
	sbID := parts[0]
	if sbID == "" {
		t.Fatal("no sandbox ID in output")
	}
	t.Logf("created: %s", strings.TrimSpace(stdout))

	// Cleanup
	t.Cleanup(func() { c.run("destroy", sbID) })
}

func TestCLIList(t *testing.T) {
	c := setupCLITest(t)

	// Create a sandbox
	stdout, _, _ := c.run("create", "--name", "cli-test-list")
	sbID := strings.Fields(stdout)[0]
	t.Cleanup(func() { c.run("destroy", sbID) })

	// List
	stdout, _, code := c.run("list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "cli-test-list") {
		t.Errorf("sandbox not in list: %s", stdout)
	}
	t.Logf("list output:\n%s", stdout)
}

func TestCLIExec(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, _ := c.run("create", "--name", "cli-test-exec")
	sbID := strings.Fields(stdout)[0]
	t.Cleanup(func() { c.run("destroy", sbID) })

	stdout, stderr, code := c.run("exec", "cli-test-exec", "--", "echo", "hello from cli")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "hello from cli" {
		t.Errorf("stdout: %q", stdout)
	}
}

func TestCLIExecFailure(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, _ := c.run("create", "--name", "cli-test-execfail")
	sbID := strings.Fields(stdout)[0]
	t.Cleanup(func() { c.run("destroy", sbID) })

	_, _, code := c.run("exec", "cli-test-execfail", "--", "false")
	if code != 1 {
		t.Errorf("exit code: %d, want 1", code)
	}
}

func TestCLIExecByName(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, _ := c.run("create", "--name", "cli-test-byname")
	sbID := strings.Fields(stdout)[0]
	t.Cleanup(func() { c.run("destroy", sbID) })

	// Resolve by name
	stdout, _, code := c.run("exec", "cli-test-byname", "--", "hostname")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(stdout) != "cli-test-byname" {
		t.Errorf("hostname: %q, want cli-test-byname", strings.TrimSpace(stdout))
	}
}

func TestCLIDestroy(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, _ := c.run("create", "--name", "cli-test-destroy")
	sbID := strings.Fields(stdout)[0]

	stdout, _, code := c.run("destroy", sbID)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "destroyed") {
		t.Errorf("output: %q", stdout)
	}

	// Verify gone from list
	stdout, _, _ = c.run("list")
	if strings.Contains(stdout, "cli-test-destroy") {
		t.Errorf("sandbox still in list after destroy")
	}
}

func TestCLIDestroyByName(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, _ := c.run("create", "--name", "cli-test-rmname")
	_ = strings.Fields(stdout)[0]

	stdout, _, code := c.run("rm", "cli-test-rmname")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, "destroyed") {
		t.Errorf("output: %q", stdout)
	}
}

func TestCLIFileWriteRead(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, _ := c.run("create", "--name", "cli-test-file")
	sbID := strings.Fields(stdout)[0]
	t.Cleanup(func() { c.run("destroy", sbID) })

	// Write via stdin piped to the binary
	content := "hello from cli file write"
	cmd := exec.Command(c.bhatti, "file", "write", "cli-test-file", "/workspace/test.txt")
	cmd.Env = append(os.Environ(), "BHATTI_URL="+c.baseURL, "BHATTI_TOKEN="+c.token)
	cmd.Stdin = strings.NewReader(content)
	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("file write: %v", err)
	}
	if !strings.Contains(outBuf.String(), "ok") {
		t.Errorf("write output: %q", outBuf.String())
	}

	// Read
	stdout, _, code := c.run("file", "read", "cli-test-file", "/workspace/test.txt")
	if code != 0 {
		t.Fatalf("file read exit %d", code)
	}
	if stdout != content {
		t.Errorf("read: %q, want %q", stdout, content)
	}
}

func TestCLIFileLS(t *testing.T) {
	c := setupCLITest(t)

	stdout, _, _ := c.run("create", "--name", "cli-test-filels")
	sbID := strings.Fields(stdout)[0]
	t.Cleanup(func() { c.run("destroy", sbID) })

	// Create some files via exec
	c.run("exec", "cli-test-filels", "--", "sh", "-c",
		"echo a > /workspace/a.txt && echo b > /workspace/b.txt && mkdir /workspace/sub")

	stdout, _, code := c.run("file", "ls", "cli-test-filels", "/workspace/")
	if code != 0 {
		t.Fatalf("file ls exit %d", code)
	}
	if !strings.Contains(stdout, "a.txt") || !strings.Contains(stdout, "b.txt") || !strings.Contains(stdout, "sub") {
		t.Errorf("ls output: %s", stdout)
	}
	t.Logf("ls output:\n%s", stdout)
}

func TestCLIPS(t *testing.T) {
	c := setupCLITest(t)

	// Create sandbox with init script (creates a session)
	stdout, _, _ := c.run("create", "--name", "cli-test-ps", "--init", "sleep 3600")
	sbID := strings.Fields(stdout)[0]
	t.Cleanup(func() { c.run("destroy", sbID) })

	time.Sleep(2 * time.Second)

	stdout, _, code := c.run("ps", "cli-test-ps")
	if code != 0 {
		t.Fatalf("ps exit %d", code)
	}
	if !strings.Contains(stdout, "init") {
		t.Errorf("init session not in ps output: %s", stdout)
	}
	t.Logf("ps output:\n%s", stdout)
}

func TestCLISecretCRUD(t *testing.T) {
	c := setupCLITest(t)

	// Set
	stdout, _, code := c.run("secret", "set", "cli-test-key", "cli-test-val")
	if code != 0 {
		t.Fatalf("secret set exit %d", code)
	}
	if !strings.Contains(stdout, "ok") {
		t.Errorf("set output: %q", stdout)
	}

	// List
	stdout, _, code = c.run("secret", "list")
	if code != 0 {
		t.Fatalf("secret list exit %d", code)
	}
	if !strings.Contains(stdout, "cli-test-key") {
		t.Errorf("secret not in list: %s", stdout)
	}

	// Delete
	stdout, _, code = c.run("secret", "delete", "cli-test-key")
	if code != 0 {
		t.Fatalf("secret delete exit %d", code)
	}

	// Verify gone
	stdout, _, _ = c.run("secret", "list")
	if strings.Contains(stdout, "cli-test-key") {
		t.Error("secret still in list after delete")
	}
}


