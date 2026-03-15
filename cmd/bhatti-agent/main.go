//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/sahilshubham/bhatti/pkg/agent/proto"
)

func main() {
	if os.Getenv("BHATTI_AGENT_TEST") == "1" {
		runTestMode()
		return
	}

	// --- PID 1 init ---

	// Mount essential filesystems.
	mustMount("proc", "/proc", "proc", 0, "")
	mustMount("sysfs", "/sys", "sysfs", 0, "")
	mustMount("devtmpfs", "/dev", "devtmpfs", 0, "")
	os.MkdirAll("/dev/pts", 0755)
	mustMount("devpts", "/dev/pts", "devpts", 0, "newinstance,ptmxmode=0666")
	mustMount("tmpfs", "/tmp", "tmpfs", 0, "")
	mustMount("tmpfs", "/run", "tmpfs", 0, "")

	syscall.Sethostname([]byte("bhatti"))

	bringUpInterface("lo")
	setupNetworking()
	installSignalHandlers()

	lnControl, err := listenVsock(proto.VsockPortControl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bhatti-agent: vsock control: %v\n", err)
		os.Exit(1)
	}
	lnForward, err := listenVsock(proto.VsockPortForward)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bhatti-agent: vsock forward: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "bhatti-agent: ready")
	go acceptLoop(lnControl, handleControlConnection)
	go acceptLoop(lnForward, handleForwardConnection)

	// PID 1 must never exit. Block forever, reaping zombies.
	for {
		var status syscall.WaitStatus
		syscall.Wait4(-1, &status, 0, nil)
	}
}

func mustMount(source, target, fstype string, flags uintptr, data string) {
	os.MkdirAll(target, 0755)
	if err := syscall.Mount(source, target, fstype, flags, data); err != nil {
		fmt.Fprintf(os.Stderr, "bhatti-agent: mount %s on %s: %v\n", source, target, err)
	}
}

func acceptLoop(ln net.Listener, handler func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "bhatti-agent: accept: %v\n", err)
			continue
		}
		go handler(conn)
	}
}

func installSignalHandlers() {
	// SIGCHLD: reap adopted orphans.
	sigchld := make(chan os.Signal, 32)
	signal.Notify(sigchld, syscall.SIGCHLD)
	go func() {
		for range sigchld {
			for {
				pid, _ := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
				if pid <= 0 {
					break
				}
			}
		}
	}()

	// SIGTERM/SIGINT: clean shutdown.
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigterm
		syscall.Sync()
		syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	}()
}
