//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/sahilshubham/bhatti/pkg/agent/proto"
)

// PTY ioctl constants (same on amd64 and arm64 Linux).
const (
	TIOCGPTN   = 0x80045430
	TIOCSPTLCK = 0x40045431
	TIOCSWINSZ = 0x5414
	TIOCSCTTY  = 0x540E
)

type winsize struct {
	Rows   uint16
	Cols   uint16
	XPixel uint16
	YPixel uint16
}

func handleTTYExec(conn net.Conn, req proto.ExecRequest) {
	master, slave, err := openPTY()
	if err != nil {
		proto.WriteFrame(conn, proto.ERROR, []byte(fmt.Sprintf("pty: %v", err)))
		return
	}
	defer master.Close()

	rows := uint16(24)
	cols := uint16(80)
	if req.Rows != nil {
		rows = *req.Rows
	}
	if req.Cols != nil {
		cols = *req.Cols
	}
	setWinsize(master, rows, cols)

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Env = buildEnv(req.Env)
	if req.Cwd != nil {
		cmd.Dir = *req.Cwd
	}
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0, // child's fd 0 (stdin) = slave PTY
	}

	if err := cmd.Start(); err != nil {
		slave.Close()
		proto.WriteFrame(conn, proto.ERROR, []byte(fmt.Sprintf("start: %v", err)))
		return
	}
	slave.Close() // parent closes slave

	// PTY master → STDOUT frames
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				proto.WriteFrame(conn, proto.STDOUT, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// conn → PTY master (STDIN, RESIZE, KILL)
	go func() {
		for {
			msgType, payload, err := proto.ReadFrame(conn)
			if err != nil {
				// Host disconnected.
				cmd.Process.Signal(syscall.SIGHUP)
				return
			}
			switch msgType {
			case proto.STDIN:
				master.Write(payload)
			case proto.RESIZE:
				if r, c, ok := proto.ParseResize(payload); ok {
					setWinsize(master, r, c)
				}
			case proto.KILL:
				cmd.Process.Signal(syscall.SIGTERM)
				return
			}
		}
	}()

	// Wait for PTY output to drain.
	<-done

	exitCode := exitCodeFromErr(cmd.Wait())
	syscall.Sync()
	exit := proto.ExitPayload(int32(exitCode))
	proto.WriteFrame(conn, proto.EXIT, exit[:])
}

func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// Get PTS number.
	var ptsNum uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		TIOCGPTN, uintptr(unsafe.Pointer(&ptsNum))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("TIOCGPTN: %v", errno)
	}

	// Unlock PTS.
	var unlock int32 = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("TIOCSPTLCK: %v", errno)
	}

	// Open slave.
	slavePath := fmt.Sprintf("/dev/pts/%d", ptsNum)
	slave, err = os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open %s: %w", slavePath, err)
	}

	return master, slave, nil
}

func setWinsize(f *os.File, rows, cols uint16) error {
	ws := winsize{Rows: rows, Cols: cols}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(),
		TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return errno
	}
	return nil
}
