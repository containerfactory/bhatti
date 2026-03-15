//go:build linux

// smoke-test connects to a running Firecracker VM via vsock and exercises
// the agent. Run after booting a VM manually.
//
// Usage: ./smoke-test <vsock-sock-path>
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sahilshubham/bhatti/pkg/agent"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <vsock-sock-path>\n", os.Args[0])
		os.Exit(1)
	}
	vsockPath := os.Args[1]

	client := agent.NewVsockClient(vsockPath)
	ctx := context.Background()

	fmt.Println("==> Waiting for agent...")
	// Manual debug: try a single Exec to see the error
	for i := 0; i < 10; i++ {
		time.Sleep(1 * time.Second)
		fmt.Printf("  attempt %d: ", i+1)
		execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := client.Exec(execCtx, []string{"echo", "hello"}, nil, "")
		cancel()
		if err != nil {
			fmt.Printf("error: %v\n", err)
			continue
		}
		fmt.Printf("exit=%d stdout=%q\n", result.ExitCode, result.Stdout)
		break
	}
	fmt.Println("==> Agent ready!")

	tests := []struct {
		name string
		argv []string
	}{
		{"uname -a", []string{"uname", "-a"}},
		{"hostname", []string{"hostname"}},
		{"whoami", []string{"whoami"}},
		{"cat /etc/os-release | head -2", []string{"sh", "-c", "head -2 /etc/os-release"}},
		{"node --version", []string{"node", "--version"}},
		{"which claude", []string{"which", "claude"}},
		{"ls /workspace", []string{"ls", "-la", "/workspace"}},
		{"echo from VM", []string{"echo", "hello from inside the VM!"}},
	}

	pass, fail := 0, 0
	for _, tt := range tests {
		result, err := client.Exec(ctx, tt.argv, nil, "")
		if err != nil {
			fmt.Printf("  ❌ %s: %v\n", tt.name, err)
			fail++
			continue
		}
		out := strings.TrimSpace(result.Stdout + result.Stderr)
		if result.ExitCode == 0 {
			fmt.Printf("  ✅ %s → %s\n", tt.name, out)
			pass++
		} else {
			fmt.Printf("  ❌ %s (exit %d) → %s\n", tt.name, result.ExitCode, out)
			fail++
		}
	}

	fmt.Printf("\n%d passed, %d failed\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
