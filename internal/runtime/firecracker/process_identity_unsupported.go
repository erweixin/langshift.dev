//go:build !linux

package firecracker

func readProcessIdentity(int) (ProcessIdentity, error) {
	return ProcessIdentity{}, ErrProcessIdentity
}
