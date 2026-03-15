//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"syscall"
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

	f := os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d", port))
	ln, err := net.FileListener(f)
	f.Close() // FileListener dups the fd
	if err != nil {
		return nil, fmt.Errorf("vsock file listener: %w", err)
	}
	return ln, nil
}

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
