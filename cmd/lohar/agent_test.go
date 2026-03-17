//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sahilshubham/bhatti/pkg/agent/proto"
)

// startTestAgent starts the agent as a subprocess in test mode over Unix sockets.
// Returns the socket paths and a cleanup function.
func startTestAgent(t *testing.T) (controlSock, forwardSock string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	controlSock = filepath.Join(dir, "control.sock")
	forwardSock = filepath.Join(dir, "forward.sock")

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperAgent")
	cmd.Env = append(os.Environ(),
		"LOHAR_TEST=1",
		"LOHAR_SOCK="+controlSock,
		"LOHAR_FWD_SOCK="+forwardSock,
		"GO_WANT_HELPER_PROCESS=1",
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}

	// Wait for socket to exist.
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(controlSock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return controlSock, forwardSock, func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
}

// TestHelperAgent is the subprocess entry point — not a real test.
func TestHelperAgent(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	runTestMode()
}

// dialControl opens a new connection to the control socket.
func dialControl(t *testing.T, sock string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}
	return conn
}

// sendExec sends an EXEC_REQ and returns the connection for reading frames.
func sendExec(t *testing.T, conn net.Conn, req proto.ExecRequest) {
	t.Helper()
	if err := proto.SendJSON(conn, proto.EXEC_REQ, req); err != nil {
		t.Fatalf("send exec: %v", err)
	}
}

// readAllExec reads all frames until EXIT, collecting stdout/stderr.
func readAllExec(t *testing.T, conn net.Conn) (stdout, stderr string, exitCode int32) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	for {
		msgType, payload, err := proto.ReadFrame(conn)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		switch msgType {
		case proto.STDOUT:
			outBuf.Write(payload)
		case proto.STDERR:
			errBuf.Write(payload)
		case proto.EXIT:
			code, ok := proto.ParseExitCode(payload)
			if !ok {
				t.Fatal("bad exit payload")
			}
			return outBuf.String(), errBuf.String(), code
		case proto.ERROR:
			t.Fatalf("agent error: %s", payload)
		}
	}
}

// --- Non-TTY exec tests ---

func TestAgentExec(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{Argv: []string{"echo", "hello"}})
	stdout, stderr, code := readAllExec(t, conn)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if stdout != "hello\n" {
		t.Errorf("stdout: %q, want %q", stdout, "hello\n")
	}
	if stderr != "" {
		t.Errorf("stderr: %q, want empty", stderr)
	}
}

func TestAgentExecFailure(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{Argv: []string{"false"}})
	_, _, code := readAllExec(t, conn)

	if code != 1 {
		t.Errorf("exit code: %d, want 1", code)
	}
}

func TestAgentExecNotFound(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{Argv: []string{"/nonexistent"}})

	// Agent should send ERROR frame (can't spawn process).
	msgType, payload, err := proto.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msgType != proto.ERROR {
		t.Fatalf("expected ERROR frame, got 0x%02x: %s", msgType, payload)
	}
}

func TestAgentExecStderr(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{Argv: []string{"sh", "-c", "echo err >&2"}})
	stdout, stderr, code := readAllExec(t, conn)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if stdout != "" {
		t.Errorf("stdout: %q, want empty", stdout)
	}
	if stderr != "err\n" {
		t.Errorf("stderr: %q, want %q", stderr, "err\n")
	}
}

func TestAgentExecEnv(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{
		Argv: []string{"sh", "-c", "echo $FOO"},
		Env:  map[string]string{"FOO": "bar"},
	})
	stdout, _, code := readAllExec(t, conn)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if stdout != "bar\n" {
		t.Errorf("stdout: %q, want %q", stdout, "bar\n")
	}
}

func TestAgentExecCwd(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	cwd := "/tmp"
	sendExec(t, conn, proto.ExecRequest{Argv: []string{"pwd"}, Cwd: &cwd})
	stdout, _, code := readAllExec(t, conn)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if strings.TrimSpace(stdout) != "/tmp" {
		t.Errorf("stdout: %q, want /tmp", stdout)
	}
}

func TestAgentExecLargeOutput(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	// Generate 1MB of output.
	sendExec(t, conn, proto.ExecRequest{
		Argv: []string{"dd", "if=/dev/zero", "bs=1024", "count=1024", "status=none"},
	})
	stdout, _, code := readAllExec(t, conn)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if len(stdout) != 1024*1024 {
		t.Errorf("stdout len: %d, want %d", len(stdout), 1024*1024)
	}
}

func TestAgentKill(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{Argv: []string{"sleep", "60"}})
	time.Sleep(100 * time.Millisecond)

	// Send KILL frame.
	if err := proto.WriteFrame(conn, proto.KILL, nil); err != nil {
		t.Fatalf("write KILL: %v", err)
	}

	_, _, code := readAllExec(t, conn)
	// SIGTERM = signal 15, exit code = 128 + 15 = 143
	if code != 143 {
		t.Errorf("exit code: %d, want 143 (128+SIGTERM)", code)
	}
}

// --- Stdin piping ---

func TestAgentExecStdin(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	// Use head -n2 instead of cat, so it exits after reading 2 lines
	// without needing to close the connection.
	sendExec(t, conn, proto.ExecRequest{Argv: []string{"head", "-n2"}})

	proto.WriteFrame(conn, proto.STDIN, []byte("line1\n"))
	proto.WriteFrame(conn, proto.STDIN, []byte("line2\n"))

	stdout, _, code := readAllExec(t, conn)
	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if stdout != "line1\nline2\n" {
		t.Errorf("stdout: %q, want %q", stdout, "line1\nline2\n")
	}
}

// --- Mixed stdout + stderr ---

func TestAgentExecMixedOutput(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{
		Argv: []string{"sh", "-c", "echo out1; echo err1 >&2; echo out2; echo err2 >&2"},
	})
	stdout, stderr, code := readAllExec(t, conn)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if !strings.Contains(stdout, "out1") || !strings.Contains(stdout, "out2") {
		t.Errorf("stdout: %q, want out1 and out2", stdout)
	}
	if !strings.Contains(stderr, "err1") || !strings.Contains(stderr, "err2") {
		t.Errorf("stderr: %q, want err1 and err2", stderr)
	}
}

// --- Default environment ---

func TestAgentExecDefaultEnv(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	sendExec(t, conn, proto.ExecRequest{
		Argv: []string{"sh", "-c", "echo PATH=$PATH; echo TERM=$TERM; echo HOME=$HOME; echo LANG=$LANG"},
	})
	stdout, _, code := readAllExec(t, conn)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if !strings.Contains(stdout, "PATH=/usr/local/sbin") {
		t.Errorf("missing default PATH in: %q", stdout)
	}
	if !strings.Contains(stdout, "TERM=xterm-256color") {
		t.Errorf("missing default TERM in: %q", stdout)
	}
	if !strings.Contains(stdout, "HOME=/root") {
		t.Errorf("missing default HOME in: %q", stdout)
	}
	if !strings.Contains(stdout, "LANG=en_US.UTF-8") {
		t.Errorf("missing default LANG in: %q", stdout)
	}
}

// --- Multiple commands on same agent (concurrency) ---

func TestAgentMultipleCommands(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	// Run 5 commands concurrently on the same agent, each on its own connection.
	const n = 5
	type result struct {
		stdout string
		code   int32
	}
	results := make(chan result, n)

	for i := 0; i < n; i++ {
		go func(i int) {
			conn := dialControl(t, ctrl)
			defer conn.Close()

			sendExec(t, conn, proto.ExecRequest{
				Argv: []string{"sh", "-c", "echo " + strings.Repeat("x", 100)},
			})
			stdout, _, code := readAllExec(t, conn)
			results <- result{stdout, code}
		}(i)
	}

	for i := 0; i < n; i++ {
		r := <-results
		if r.code != 0 {
			t.Errorf("command %d: exit code %d", i, r.code)
		}
		expected := strings.Repeat("x", 100) + "\n"
		if r.stdout != expected {
			t.Errorf("command %d: stdout length %d, want %d", i, len(r.stdout), len(expected))
		}
	}
}

// --- Host disconnect during exec ---

func TestAgentHostDisconnect(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)

	// Start a long-running command.
	sendExec(t, conn, proto.ExecRequest{Argv: []string{"sleep", "60"}})
	time.Sleep(100 * time.Millisecond)

	// Abruptly close the connection. The agent should clean up.
	conn.Close()

	// Verify the agent is still healthy by running another command.
	time.Sleep(200 * time.Millisecond)
	conn2 := dialControl(t, ctrl)
	defer conn2.Close()

	sendExec(t, conn2, proto.ExecRequest{Argv: []string{"echo", "still alive"}})
	stdout, _, code := readAllExec(t, conn2)

	if code != 0 {
		t.Errorf("exit code: %d", code)
	}
	if strings.TrimSpace(stdout) != "still alive" {
		t.Errorf("stdout: %q", stdout)
	}
}

// --- TTY tests ---

// ttyFrame is a frame received from a TTY connection.
type ttyFrame struct {
	msgType byte
	payload []byte
	err     error
}

// startTTYReader spawns a goroutine that reads frames from conn
// and sends them to the returned channel. Avoids SetReadDeadline which
// corrupts frame boundaries when io.ReadFull gets a timeout mid-read.
func startTTYReader(conn net.Conn) <-chan ttyFrame {
	ch := make(chan ttyFrame, 64)
	go func() {
		defer close(ch)
		for {
			msgType, payload, err := proto.ReadFrame(conn)
			if err != nil {
				ch <- ttyFrame{err: err}
				return
			}
			ch <- ttyFrame{msgType: msgType, payload: payload}
		}
	}()
	return ch
}

// waitForOutput reads frames from ch until the accumulated STDOUT contains substr,
// or the timeout expires. Also returns early on ERROR or EXIT frames.
func waitForOutput(t *testing.T, ch <-chan ttyFrame, substr string, timeout time.Duration) (output string, exitCode *int32) {
	t.Helper()
	var buf bytes.Buffer
	timer := time.After(timeout)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed, output so far: %q", buf.String())
			}
			if f.err != nil {
				t.Fatalf("read error: %v, output so far: %q", f.err, buf.String())
			}
			switch f.msgType {
			case proto.STDOUT:
				buf.Write(f.payload)
				if substr != "" && strings.Contains(buf.String(), substr) {
					return buf.String(), nil
				}
			case proto.EXIT:
				code, _ := proto.ParseExitCode(f.payload)
				return buf.String(), &code
			case proto.ERROR:
				t.Fatalf("agent error: %s", f.payload)
			}
		case <-timer:
			t.Fatalf("timeout waiting for %q, output so far: %q", substr, buf.String())
		}
	}
}

func TestAgentTTY(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	ttyTrue := true
	sendExec(t, conn, proto.ExecRequest{
		Argv: []string{"/bin/sh"},
		TTY:  &ttyTrue,
	})

	ch := startTTYReader(conn)

	// Give the shell a moment to start, then send command.
	time.Sleep(200 * time.Millisecond)
	proto.WriteFrame(conn, proto.STDIN, []byte("echo hello\n"))

	// Wait for "hello" in output.
	waitForOutput(t, ch, "hello", 5*time.Second)

	// Exit the shell.
	proto.WriteFrame(conn, proto.STDIN, []byte("exit\n"))

	// Wait for EXIT frame.
	timer := time.After(5 * time.Second)
	for {
		select {
		case f := <-ch:
			if f.err != nil {
				return // connection closed, that's fine
			}
			if f.msgType == proto.EXIT {
				code, _ := proto.ParseExitCode(f.payload)
				if code != 0 {
					t.Errorf("exit code: %d, want 0", code)
				}
				return
			}
		case <-timer:
			t.Fatal("timeout waiting for EXIT")
		}
	}
}

func TestAgentTTYResize(t *testing.T) {
	ctrl, _, cleanup := startTestAgent(t)
	defer cleanup()

	conn := dialControl(t, ctrl)
	defer conn.Close()

	ttyTrue := true
	rows := uint16(24)
	cols := uint16(80)
	sendExec(t, conn, proto.ExecRequest{
		Argv: []string{"/bin/sh"},
		TTY:  &ttyTrue,
		Rows: &rows,
		Cols: &cols,
	})

	ch := startTTYReader(conn)
	time.Sleep(200 * time.Millisecond)

	// Send RESIZE.
	resize := proto.ResizePayload(40, 120)
	proto.WriteFrame(conn, proto.RESIZE, resize[:])
	time.Sleep(100 * time.Millisecond)

	// Ask for terminal size.
	proto.WriteFrame(conn, proto.STDIN, []byte("stty size\n"))

	// Wait for "40 120" in output.
	waitForOutput(t, ch, "40 120", 5*time.Second)

	// Clean exit.
	proto.WriteFrame(conn, proto.STDIN, []byte("exit\n"))
	timer := time.After(5 * time.Second)
	for {
		select {
		case f := <-ch:
			if f.err != nil || f.msgType == proto.EXIT {
				return
			}
		case <-timer:
			return // timeout is ok, we verified resize worked
		}
	}
}

// --- Port forwarding tests ---

func TestAgentForward(t *testing.T) {
	_, fwdSock, cleanup := startTestAgent(t)
	defer cleanup()

	// Start a TCP echo server on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn) // echo
	}()

	// Connect to forward socket.
	conn, err := net.Dial("unix", fwdSock)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()

	// Send FWD_REQ.
	proto.SendJSON(conn, proto.FWD_REQ, proto.ForwardRequest{Port: uint16(port)})

	// Read FWD_RESP.
	msgType, payload, err := proto.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read fwd resp: %v", err)
	}
	if msgType != proto.FWD_RESP {
		t.Fatalf("expected FWD_RESP, got 0x%02x", msgType)
	}
	var resp proto.ForwardResponse
	json.Unmarshal(payload, &resp)
	if resp.Status != "ok" {
		t.Fatalf("forward status: %q", resp.Status)
	}

	// After handshake, raw bytes. Write "ping", read it back.
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("echo: %q, want %q", buf, "ping")
	}
}

func TestAgentForwardLargeData(t *testing.T) {
	_, fwdSock, cleanup := startTestAgent(t)
	defer cleanup()

	// TCP server that echoes everything back.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()

	conn, err := net.Dial("unix", fwdSock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	proto.SendJSON(conn, proto.FWD_REQ, proto.ForwardRequest{Port: uint16(port)})
	msgType, payload, err := proto.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read resp: %v", err)
	}
	if msgType != proto.FWD_RESP {
		t.Fatalf("expected FWD_RESP, got 0x%02x", msgType)
	}
	var resp proto.ForwardResponse
	json.Unmarshal(payload, &resp)
	if resp.Status != "ok" {
		t.Fatalf("status: %q", resp.Status)
	}

	// Send 64KB through the tunnel and verify echo.
	data := make([]byte, 64*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	go func() {
		conn.Write(data)
	}()

	received := make([]byte, len(data))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, received); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(data, received) {
		t.Error("echoed data does not match sent data")
	}
}

func TestAgentForwardRefused(t *testing.T) {
	_, fwdSock, cleanup := startTestAgent(t)
	defer cleanup()

	conn, err := net.Dial("unix", fwdSock)
	if err != nil {
		t.Fatalf("dial forward: %v", err)
	}
	defer conn.Close()

	// Forward to a port nobody is listening on.
	proto.SendJSON(conn, proto.FWD_REQ, proto.ForwardRequest{Port: 59999})

	msgType, payload, err := proto.ReadFrame(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msgType != proto.FWD_RESP {
		t.Fatalf("expected FWD_RESP, got 0x%02x", msgType)
	}
	var resp proto.ForwardResponse
	json.Unmarshal(payload, &resp)
	if resp.Status != "error" {
		t.Errorf("status: %q, want %q", resp.Status, "error")
	}
	if resp.Message == nil || !strings.Contains(*resp.Message, "refused") {
		t.Errorf("message: %v, want 'refused'", resp.Message)
	}
}
