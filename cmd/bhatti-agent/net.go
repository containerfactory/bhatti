//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// AF_VSOCK constants.
const (
	AF_VSOCK       = 40
	VMADDR_CID_ANY = 0xFFFFFFFF
)

// SockaddrVM matches struct sockaddr_vm (16 bytes).
type SockaddrVM struct {
	Family    uint16
	Reserved1 uint16
	Port      uint32
	CID       uint32
	Flags     uint8
	Zero      [3]uint8
}

func listenVsock(port uint32) (net.Listener, error) {
	// BLOCKING socket with a receive timeout. The timeout makes accept4
	// return EAGAIN periodically (kernel-level timer, not Go runtime).
	// This is essential for snapshot/resume: after restore, Go's runtime
	// (timers, netpoller, goroutine scheduler) is stalled. The kernel
	// timer is the only thing that fires reliably, giving us a heartbeat
	// to detect resume (via clock jump) and recreate the listener.
	fd, err := syscall.Socket(AF_VSOCK, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}

	addr := SockaddrVM{
		Family: AF_VSOCK,
		Port:   port,
		CID:    VMADDR_CID_ANY,
	}
	_, _, errno := syscall.Syscall(syscall.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(&addr)), unsafe.Sizeof(addr))
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("vsock bind port %d: %v", port, errno)
	}

	if err := syscall.Listen(fd, 128); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("vsock listen: %w", err)
	}

	// Set accept timeout so accept4 returns EAGAIN every second.
	// This kernel-level timer fires even when Go's runtime is stalled
	// (as happens after Firecracker snapshot/resume).
	tv := syscall.Timeval{Sec: 1}
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	return &vsockListener{fd: fd, port: port}, nil
}

// vsockListener implements net.Listener for AF_VSOCK using a blocking socket.
// The accept4 syscall blocks in the kernel, which survives snapshot/resume.
type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	for {
		before := time.Now()
		nfd, _, errno := syscall.Syscall6(syscall.SYS_ACCEPT4,
			uintptr(l.fd), 0, 0,
			uintptr(syscall.SOCK_CLOEXEC), 0, 0)
		if errno == 0 {
			f := os.NewFile(nfd, fmt.Sprintf("vsock-conn:%d", l.port))
			return &vsockConn{file: f}, nil
		}
		if errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK {
			// accept4 timed out (SO_RCVTIMEO). Check for resume:
			// if wall clock jumped far beyond our 1-second timeout,
			// the VM was paused and resumed.
			elapsed := time.Since(before)
			if elapsed > 3*time.Second {
				logf("vsock port %d: resume detected (accept took %v), re-creating listener", l.port, elapsed)
				time.Sleep(200 * time.Millisecond) // let transport reset settle
				if err := l.recreate(); err != nil {
					logf("vsock port %d: re-create failed: %v", l.port, err)
				} else {
					logf("vsock port %d: listener re-created", l.port)
				}
			}
			continue
		}
		return nil, fmt.Errorf("vsock accept: %v", errno)
	}
}

// recreate closes the current fd and creates a fresh vsock listener.
func (l *vsockListener) recreate() error {
	syscall.Close(l.fd)

	fd, err := syscall.Socket(AF_VSOCK, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)

	addr := SockaddrVM{Family: AF_VSOCK, Port: l.port, CID: VMADDR_CID_ANY}
	_, _, errno := syscall.Syscall(syscall.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(&addr)), unsafe.Sizeof(addr))
	if errno != 0 {
		syscall.Close(fd)
		return fmt.Errorf("bind: %v", errno)
	}
	if err := syscall.Listen(fd, 128); err != nil {
		syscall.Close(fd)
		return err
	}
	tv := syscall.Timeval{Sec: 1}
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	l.fd = fd
	return nil
}

func (l *vsockListener) Close() error {
	return syscall.Close(l.fd)
}

func (l *vsockListener) Addr() net.Addr {
	return vsockAddr(l.port)
}

// vsockConn wraps an os.File as a net.Conn for AF_VSOCK connections.
type vsockConn struct {
	file *os.File
}

func (c *vsockConn) Read(b []byte) (int, error)         { return c.file.Read(b) }
func (c *vsockConn) Write(b []byte) (int, error)        { return c.file.Write(b) }
func (c *vsockConn) Close() error                       { return c.file.Close() }
func (c *vsockConn) LocalAddr() net.Addr                { return vsockAddr(0) }
func (c *vsockConn) RemoteAddr() net.Addr               { return vsockAddr(0) }
func (c *vsockConn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *vsockConn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }

type vsockAddr uint32

func (a vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string  { return fmt.Sprintf("vsock://:%d", uint32(a)) }

// Networking ioctl constants.
const (
	SIOCGIFFLAGS = 0x8913
	SIOCSIFFLAGS = 0x8914
	IFF_UP       = 0x1
	IFF_RUNNING  = 0x40
)

func bringUpInterface(name string) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		logf("socket for %s: %v", name, err)
		return
	}
	defer syscall.Close(fd)

	var ifr [40]byte
	copy(ifr[:], name)

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); errno != 0 {
		logf("SIOCGIFFLAGS %s: %v", name, errno)
		return
	}

	flags := binary.LittleEndian.Uint16(ifr[16:18])
	flags |= IFF_UP | IFF_RUNNING
	binary.LittleEndian.PutUint16(ifr[16:18], flags)

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); errno != 0 {
		logf("SIOCSIFFLAGS %s: %v", name, errno)
	}
}

func setupNetworking() {
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			logf("net: %s: %s", iface.Name, addr)
		}
	}
}
