package firecracker

import (
	"errors"
	"testing"
)

func validSpec() Spec {
	return Spec{
		MachineID:       "runtime-019f60b2",
		KernelImagePath: "/images/vmlinux-6.12.0",
		RootDrivePath:   "/images/rootfs.ext4",
		ScratchPath:     "/drives/scratch.ext4",
		VSockPath:       "/run/guest.vsock",
		GuestCID:        42,
		VCPUCount:       2,
		MemoryMiB:       512,
		ScratchLimit: RateLimit{
			Bandwidth:  TokenBucket{Size: 16 << 20, RefillMillis: 1000, Burst: 4 << 20},
			Operations: TokenBucket{Size: 2000, RefillMillis: 1000, Burst: 500},
		},
	}
}

func TestSpecRequiresBoundedNoNetworkMachineInputs(t *testing.T) {
	if err := validSpec().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*Spec)
	}{
		{name: "unsafe id", edit: func(value *Spec) { value.MachineID = "../tenant" }},
		{name: "relative kernel", edit: func(value *Spec) { value.KernelImagePath = "kernel" }},
		{name: "aliased disks", edit: func(value *Spec) { value.ScratchPath = value.RootDrivePath }},
		{name: "reserved cid", edit: func(value *Spec) { value.GuestCID = 2 }},
		{name: "excess vcpu", edit: func(value *Spec) { value.VCPUCount = 33 }},
		{name: "small memory", edit: func(value *Spec) { value.MemoryMiB = 126 }},
		{name: "unbounded disk", edit: func(value *Spec) { value.ScratchLimit.Operations.Size = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validSpec()
			test.edit(&value)
			if err := value.Validate(); !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}
