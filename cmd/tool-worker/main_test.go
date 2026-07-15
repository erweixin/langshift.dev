package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostInventoryIsStrictAndRejectsRemotePlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	valid := `{"schema_version":1,"hosts":{"runtime-host-1":{"url":"https://runtime-host-1.internal:8443","kernel_digest":"` + digestA + `","rootfs_digest":"` + digestB + `"}}}`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	hosts, err := loadHostEndpoints(path, false)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("hosts=%#v err=%v", hosts, err)
	}
	invalid := `{"schema_version":1,"hosts":{"runtime-host-1":{"url":"http://runtime-host-1.internal:8443","kernel_digest":"` + digestA + `","rootfs_digest":"` + digestB + `"}}}`
	if err = os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadHostEndpoints(path, true); err == nil {
		t.Fatal("remote plaintext Runtime Host accepted")
	}
	unknown := valid[:len(valid)-2] + `,"unexpected":true}}`
	if err = os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadHostEndpoints(path, false); err == nil {
		t.Fatal("unknown inventory field accepted")
	}
}

func TestToolWorkerConfigurationFailsClosed(t *testing.T) {
	if err := (config{}).validate(); err == nil {
		t.Fatal("empty ToolWorker configuration accepted")
	}
	if secureEndpoint("http://runtime.internal:8443", true) == nil {
		t.Fatal("remote plaintext endpoint accepted")
	}
	if secureEndpoint("http://127.0.0.1:8443", true) != nil {
		t.Fatal("explicit loopback development endpoint rejected")
	}
}
