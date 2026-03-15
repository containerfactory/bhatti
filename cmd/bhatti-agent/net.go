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
	fd, err := syscall.Socket(AF_VSOCK, syscall.SOCK_STREAM, 0)
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

	// Go's net.FileListener doesn't support AF_VSOCK, so we implement
	// a custom listener that uses raw accept() and wraps connections
	// via os.NewFile + net.FileConn.
	return &vsockListener{fd: fd, port: port}, nil
}

// vsockListener implements net.Listener using a raw AF_VSOCK socket fd.
type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	// Go's syscall.Accept tries to parse the peer sockaddr and returns
	// EAFNOSUPPORT for AF_VSOCK. Use raw accept4 syscall instead,
	// passing nil for addr/addrlen since we don't need the peer address.
	nfd, _, errno := syscall.Syscall6(syscall.SYS_ACCEPT4,
		uintptr(l.fd), 0, 0, 0 /* flags */, 0, 0)
	if errno != 0 {
		return nil, fmt.Errorf("vsock accept: %v", errno)
	}
	f := os.NewFile(nfd, fmt.Sprintf("vsock-conn:%d", l.port))
	return &vsockConn{file: f}, nil
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

func (l *vsockListener) Close() error {
	return syscall.Close(l.fd)
}

func (l *vsockListener) Addr() net.Addr {
	return vsockAddr(l.port)
}

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

	var ifr [40]byte // struct ifreq
	copy(ifr[:], name)

	// Get current flags.
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); errno != 0 {
		logf("SIOCGIFFLAGS %s: %v", name, errno)
		return
	}

	// Set IFF_UP | IFF_RUNNING.
	flags := binary.LittleEndian.Uint16(ifr[16:18])
	flags |= IFF_UP | IFF_RUNNING
	binary.LittleEndian.PutUint16(ifr[16:18], flags)

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd),
		SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); errno != 0 {
		logf("SIOCSIFFLAGS %s: %v", name, errno)
	}
}

func setupNetworking() {
	// Network config is handled by the kernel's ip= cmdline parameter.
	// Log interfaces for debugging.
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
