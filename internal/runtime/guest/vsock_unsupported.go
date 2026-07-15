//go:build !linux

package guest

import "net"

func ListenVSock(uint32) (net.Listener, error) { return nil, ErrExecutionPolicy }
