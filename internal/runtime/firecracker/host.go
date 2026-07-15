package firecracker

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

var ErrUntrustedHostPath = errors.New("Firecracker host path is not trusted")

type HostPathInfo struct {
	Mode fs.FileMode
	UID  uint32
}

type HostPathInspector interface {
	Lstat(path string) (HostPathInfo, error)
}

type hostPathKind uint8

const (
	hostExecutable hostPathKind = iota
	hostDirectory
	hostNamespace
)

func (config JailerConfig) VerifyHostPaths(inspector HostPathInspector) error {
	if inspector == nil {
		return ErrUntrustedHostPath
	}
	paths := []struct {
		value string
		kind  hostPathKind
	}{
		{value: config.JailerPath, kind: hostExecutable},
		{value: config.FirecrackerPath, kind: hostExecutable},
		{value: config.ChrootBaseDir, kind: hostDirectory},
	}
	if config.NetworkNamespace != "" {
		paths = append(paths, struct {
			value string
			kind  hostPathKind
		}{value: config.NetworkNamespace, kind: hostNamespace})
	}
	for _, path := range paths {
		if !trustedAbsolute(path.value) {
			return ErrUntrustedHostPath
		}
		if err := verifyHostPath(inspector, path.value, path.kind); err != nil {
			return err
		}
	}
	return nil
}

func verifyHostPath(inspector HostPathInspector, path string, kind hostPathKind) error {
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	current := string(filepath.Separator)
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := inspector.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: inspect %s", ErrUntrustedHostPath, current)
		}
		if info.UID != 0 || info.Mode&fs.ModeSymlink != 0 || info.Mode.Perm()&0o022 != 0 {
			return fmt.Errorf("%w: ownership or mode %s", ErrUntrustedHostPath, current)
		}
		last := index == len(components)-1
		if !last && !info.Mode.IsDir() {
			return fmt.Errorf("%w: parent is not a directory %s", ErrUntrustedHostPath, current)
		}
		if !last {
			continue
		}
		switch kind {
		case hostExecutable:
			if !info.Mode.IsRegular() || info.Mode.Perm()&0o111 == 0 {
				return fmt.Errorf("%w: executable %s", ErrUntrustedHostPath, current)
			}
		case hostDirectory:
			if !info.Mode.IsDir() {
				return fmt.Errorf("%w: directory %s", ErrUntrustedHostPath, current)
			}
		case hostNamespace:
			if !info.Mode.IsRegular() {
				return fmt.Errorf("%w: network namespace %s", ErrUntrustedHostPath, current)
			}
		default:
			return ErrUntrustedHostPath
		}
	}
	return nil
}
