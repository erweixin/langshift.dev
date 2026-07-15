//go:build linux

package guest

import (
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

func ListenVSock(port uint32) (net.Listener, error) {
	if port < 1024 {
		return nil, ErrExecutionPolicy
	}
	descriptor, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = unix.Close(descriptor)
		}
	}()
	if err = unix.Bind(descriptor, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		return nil, err
	}
	if err = unix.Listen(descriptor, 16); err != nil {
		return nil, err
	}
	failed = false
	return &vsockListener{descriptor: descriptor, port: port}, nil
}

type vsockListener struct {
	descriptor int
	port       uint32
	closed     atomic.Bool
}

func (listener *vsockListener) Accept() (net.Conn, error) {
	descriptor, address, err := unix.Accept4(listener.descriptor, unix.SOCK_CLOEXEC)
	if err != nil {
		return nil, err
	}
	remote, ok := address.(*unix.SockaddrVM)
	if !ok {
		_ = unix.Close(descriptor)
		return nil, ErrExecutionPolicy
	}
	file := os.NewFile(uintptr(descriptor), fmt.Sprintf("vsock-%d-%d", remote.CID, remote.Port))
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, ErrExecutionPolicy
	}
	return &vsockConnection{File: file, local: vsockAddress{cid: unix.VMADDR_CID_ANY, port: listener.port}, remote: vsockAddress{cid: remote.CID, port: remote.Port}}, nil
}

func (listener *vsockListener) Close() error {
	if !listener.closed.CompareAndSwap(false, true) {
		return net.ErrClosed
	}
	return unix.Close(listener.descriptor)
}

func (listener *vsockListener) Addr() net.Addr {
	return vsockAddress{cid: unix.VMADDR_CID_ANY, port: listener.port}
}

type vsockConnection struct {
	*os.File
	local, remote net.Addr
}

func (connection *vsockConnection) LocalAddr() net.Addr  { return connection.local }
func (connection *vsockConnection) RemoteAddr() net.Addr { return connection.remote }
func (connection *vsockConnection) SetDeadline(value time.Time) error {
	return connection.File.SetDeadline(value)
}
func (connection *vsockConnection) SetReadDeadline(value time.Time) error {
	return connection.File.SetReadDeadline(value)
}
func (connection *vsockConnection) SetWriteDeadline(value time.Time) error {
	return connection.File.SetWriteDeadline(value)
}

type vsockAddress struct{ cid, port uint32 }

func (address vsockAddress) Network() string { return "vsock" }
func (address vsockAddress) String() string  { return fmt.Sprintf("%d:%d", address.cid, address.port) }
