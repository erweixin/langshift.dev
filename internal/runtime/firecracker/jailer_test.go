package firecracker

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
)

type staticHostInspector struct {
	overrides map[string]HostPathInfo
}

func (inspector staticHostInspector) Lstat(path string) (HostPathInfo, error) {
	if value, found := inspector.overrides[path]; found {
		return value, nil
	}
	return HostPathInfo{Mode: fs.ModeDir | 0o755, UID: 0}, nil
}

func validJailerConfig() JailerConfig {
	return JailerConfig{
		JailerPath:      "/opt/lites/firecracker-v1.15.1/jailer",
		FirecrackerPath: "/opt/lites/firecracker-v1.15.1/firecracker",
		ChrootBaseDir:   "/srv/lites/jailer",
		UID:             10001, GID: 10001,
		ParentCgroup:   "lites/firecracker",
		CPUQuotaMicros: 100000, CPUPeriodMicros: 100000,
		MemoryMaxBytes: 768 << 20, PidsMax: 256,
		FileSizeMaxBytes: 2 << 30, NoFileMax: 1024,
		PathInspector: staticHostInspector{overrides: map[string]HostPathInfo{
			"/opt/lites/firecracker-v1.15.1/jailer":      {Mode: 0o555, UID: 0},
			"/opt/lites/firecracker-v1.15.1/firecracker": {Mode: 0o555, UID: 0},
		}},
	}
}

func TestJailerUsesCgroupV2NoSwapAndNewPIDNamespace(t *testing.T) {
	config := validJailerConfig()
	args, err := config.Arguments("runtime-019f60b2")
	if err != nil {
		t.Fatal(err)
	}
	wantFragments := [][]string{
		{"--cgroup-version", "2"}, {"--cgroup", "cpu.max=100000 100000"}, {"--cgroup", "memory.max=805306368"}, {"--cgroup", "memory.swap.max=0"}, {"--cgroup", "pids.max=256"}, {"--new-pid-ns"}, {"--", "--api-sock", "/run/firecracker.socket", "--http-api-max-payload-size", "4096"},
	}
	for _, fragment := range wantFragments {
		if !containsSequence(args, fragment) {
			t.Fatalf("arguments do not contain %#v: %#v", fragment, args)
		}
	}
	command, err := config.Command(context.Background(), "runtime-019f60b2")
	if err != nil || command.Path != config.JailerPath || len(command.Env) != 0 {
		t.Fatalf("Command() = %#v, %v", command, err)
	}
	root, err := config.JailRoot("runtime-019f60b2")
	wantRoot := filepath.Join(config.ChrootBaseDir, "firecracker", "runtime-019f60b2", "root")
	if err != nil || root != wantRoot {
		t.Fatalf("JailRoot() = %q, %v", root, err)
	}
	config.CgroupBaseDir = "/sys/fs/cgroup"
	cgroup, err := config.CgroupPath("runtime-019f60b2")
	if err != nil || cgroup != "/sys/fs/cgroup/lites/firecracker/runtime-019f60b2" {
		t.Fatalf("CgroupPath() = %q, %v", cgroup, err)
	}
}

func TestJailerRejectsUntrustedHostPathOwnershipModesAndSymlinks(t *testing.T) {
	tests := []struct {
		path string
		info HostPathInfo
	}{
		{path: "/opt", info: HostPathInfo{Mode: fs.ModeDir | 0o755, UID: 1000}},
		{path: "/opt/lites", info: HostPathInfo{Mode: fs.ModeDir | 0o775, UID: 0}},
		{path: "/opt/lites/firecracker-v1.15.1", info: HostPathInfo{Mode: fs.ModeSymlink | 0o777, UID: 0}},
		{path: "/opt/lites/firecracker-v1.15.1/jailer", info: HostPathInfo{Mode: 0o444, UID: 0}},
	}
	for _, test := range tests {
		config := validJailerConfig()
		base := config.PathInspector.(staticHostInspector)
		overrides := make(map[string]HostPathInfo, len(base.overrides)+1)
		for path, info := range base.overrides {
			overrides[path] = info
		}
		overrides[test.path] = test.info
		config.PathInspector = staticHostInspector{overrides: overrides}
		if _, err := config.Command(context.Background(), "runtime-019f60b2"); !errors.Is(err, ErrUntrustedHostPath) {
			t.Fatalf("Command() accepted untrusted %s: %v", test.path, err)
		}
	}
}

func TestJailerRejectsUnsafeOrUnboundedConfiguration(t *testing.T) {
	tests := []func(*JailerConfig){
		func(value *JailerConfig) { value.JailerPath = "jailer" },
		func(value *JailerConfig) { value.UID = 0 },
		func(value *JailerConfig) { value.ParentCgroup = "../escape" },
		func(value *JailerConfig) { value.MemoryMaxBytes = 64 << 20 },
		func(value *JailerConfig) { value.PidsMax = 0 },
		func(value *JailerConfig) { value.NetworkNamespace = "tenant-supplied" },
	}
	for index, edit := range tests {
		value := validJailerConfig()
		edit(&value)
		if _, err := value.Arguments("runtime-019f60b2"); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("case %d: %v", index, err)
		}
	}
}

func containsSequence(values, fragment []string) bool {
	for index := 0; index+len(fragment) <= len(values); index++ {
		if reflect.DeepEqual(values[index:index+len(fragment)], fragment) {
			return true
		}
	}
	return false
}
