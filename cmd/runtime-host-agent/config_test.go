package main

import (
	"os"
	"testing"
)

func TestRuntimeHostConfigurationFailsClosedAndAcceptsExplicitDevelopmentFixture(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "DATABASE_URL_FILE", "STORE_EPOCH_AUTHORITY_URL", "ALLOW_INSECURE_DEVELOPMENT"} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty runtime host configuration accepted")
	}
	values := map[string]string{
		"ALLOW_INSECURE_DEVELOPMENT": "true", "DATABASE_URL": "postgres://runtime@127.0.0.1/lites?sslmode=disable", "STORE_EPOCH_AUTHORITY_URL": "http://127.0.0.1:8080/v1/store-epoch",
		"RUNTIME_CAPABILITY_KEYRING_FILE": "/tmp/capability.json", "RUNTIME_ID_KEY_FILE": "/tmp/id", "RUNTIME_TOKEN_PEPPER_FILE": "/tmp/pepper", "RUNTIME_HOST_CONTROL_TOKEN_FILE": "/tmp/control", "RUNTIME_OWNERSHIP_KEY_FILE": "/tmp/ownership",
		"RUNTIME_HOST_ID": "host-1", "RUNTIME_POOL_KEY": "runtime-untrusted", "RUNTIME_ARCHITECTURE": "x86_64", "RUNTIME_AVAILABILITY_ZONE": "zone-a", "RUNTIME_KERNEL_CATALOG_HASH": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "RUNTIME_ROOTFS_CATALOG_HASH": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "RUNTIME_SCRATCH_DIGEST": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"JAILER_PATH": "/usr/bin/jailer", "JAILER_DIGEST": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "FIRECRACKER_PATH": "/usr/bin/firecracker", "FIRECRACKER_BINARY_DIGEST": "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "FIRECRACKER_CHROOT_BASE_DIR": "/srv/firecracker", "FIRECRACKER_PARENT_CGROUP": "lites/runtime", "FIRECRACKER_KERNEL_PATH": "/srv/assets/vmlinux", "FIRECRACKER_KERNEL_DIGEST": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "FIRECRACKER_ROOTFS_PATH": "/srv/assets/rootfs.ext4", "FIRECRACKER_ROOTFS_DIGEST": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "FIRECRACKER_SCRATCH_PATH": "/srv/assets/scratch.ext4",
		"S3_REGION": "us-east-1", "S3_PAYLOAD_BUCKET": "lites-payloads", "VAULT_ADDR": "http://127.0.0.1:8200", "LISTEN_ADDRESS": "127.0.0.1:8443", "HEALTH_ADDRESS": "127.0.0.1:8085", "LITES_ENVIRONMENT": "test", "LITES_VERSION": "test", "LITES_REGION": "US",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	configuration, err := loadConfig()
	if err != nil || configuration.hostID != "host-1" || !configuration.allowInsecureDevelopment {
		t.Fatalf("loadConfig()=%#v err=%v env=%s", configuration, err, os.Getenv("ALLOW_INSECURE_DEVELOPMENT"))
	}
	t.Setenv("STORE_EPOCH_AUTHORITY_URL", "http://epoch.internal/v1/store-epoch")
	if _, err = loadConfig(); err == nil {
		t.Fatal("remote plaintext epoch authority accepted")
	}
}
