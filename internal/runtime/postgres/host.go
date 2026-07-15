package postgres

import (
	"context"
	"errors"
	"time"
)

type HostRegistration struct {
	HostID, PoolKey, Architecture, AvailabilityZone     string
	KernelCatalogHash, RootFSCatalogHash, ScratchDigest string
	ControlTokenHash                                    []byte
	CapacityVCPU, CapacityMemoryMiB, CapacityDiskMiB    int
	CapacitySessions                                    int
	ObservedAt, HeartbeatDeadline                       time.Time
}

func (store Store) RegisterHost(ctx context.Context, command HostRegistration) (uint64, error) {
	if !store.valid() || !validHostRegistration(command) {
		return 0, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return 0, err
	}
	var version uint64
	err := store.Pool.QueryRow(ctx, `SELECT agent.runtime_register_host($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		command.HostID, command.PoolKey, command.Architecture, command.AvailabilityZone, command.KernelCatalogHash, command.RootFSCatalogHash, command.ScratchDigest, command.ControlTokenHash,
		command.CapacityVCPU, command.CapacityMemoryMiB, command.CapacityDiskMiB, command.CapacitySessions, command.ObservedAt.UTC().Truncate(time.Microsecond), command.HeartbeatDeadline.UTC().Truncate(time.Microsecond)).Scan(&version)
	if err != nil || version == 0 {
		return 0, errors.Join(err, ErrSessionConflict)
	}
	return version, nil
}

func validHostRegistration(command HostRegistration) bool {
	return command.HostID != "" && command.PoolKey != "" && command.Architecture != "" && command.AvailabilityZone != "" && command.KernelCatalogHash != "" && command.RootFSCatalogHash != "" && command.ScratchDigest != "" && len(command.ControlTokenHash) == 32 && command.CapacityVCPU > 0 && command.CapacityMemoryMiB >= 128 && command.CapacityDiskMiB >= 64 && command.CapacitySessions > 0 && !command.ObservedAt.IsZero() && command.HeartbeatDeadline.After(command.ObservedAt)
}

func (store Store) SetHostStatus(ctx context.Context, hostID string, expectedVersion uint64, controlHash []byte, status string, at, deadline time.Time) (uint64, error) {
	if !store.valid() || hostID == "" || expectedVersion == 0 || len(controlHash) != 32 || status == "" || at.IsZero() || !deadline.After(at) {
		return 0, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return 0, err
	}
	var version uint64
	err := store.Pool.QueryRow(ctx, `SELECT agent.runtime_set_host_status($1,$2,$3,$4,$5,$6)`, hostID, expectedVersion, controlHash, status, at.UTC().Truncate(time.Microsecond), deadline.UTC().Truncate(time.Microsecond)).Scan(&version)
	if err != nil || version != expectedVersion+1 {
		return 0, errors.Join(err, ErrSessionConflict)
	}
	return version, nil
}

func (store Store) HeartbeatHost(ctx context.Context, hostID string, expectedVersion uint64, controlHash []byte, at, deadline time.Time) (uint64, error) {
	if !store.valid() || hostID == "" || expectedVersion == 0 || len(controlHash) != 32 || at.IsZero() || !deadline.After(at) {
		return 0, ErrInvalidCommand
	}
	if err := store.requireEpoch(ctx); err != nil {
		return 0, err
	}
	var version uint64
	err := store.Pool.QueryRow(ctx, `SELECT agent.runtime_heartbeat_host($1,$2,$3,$4,$5)`, hostID, expectedVersion, controlHash, at.UTC().Truncate(time.Microsecond), deadline.UTC().Truncate(time.Microsecond)).Scan(&version)
	if err != nil || version != expectedVersion+1 {
		return 0, errors.Join(err, ErrSessionConflict)
	}
	return version, nil
}
