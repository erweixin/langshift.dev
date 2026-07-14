package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type sequenceEpochAuthority struct {
	mu     sync.Mutex
	values []string
	errors []error
	index  int
}

func (authority *sequenceEpochAuthority) CurrentStoreEpoch(context.Context) (string, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	index := authority.index
	if authority.index < len(authority.values)-1 {
		authority.index++
	}
	var err error
	if index < len(authority.errors) {
		err = authority.errors[index]
	}
	return authority.values[index], err
}

func TestMonitorStoreEpochIgnoresOutageAndStopsOnChange(t *testing.T) {
	authority := &sequenceEpochAuthority{values: []string{"", "epoch-a", "epoch-b"}, errors: []error{errors.New("offline")}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := monitorStoreEpoch(ctx, authority, "epoch-a", time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "epoch-a to epoch-b") {
		t.Fatalf("expected epoch rollover error, got %v", err)
	}
}

func TestMonitorStoreEpochStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := monitorStoreEpoch(ctx, &sequenceEpochAuthority{values: []string{"epoch-a"}}, "epoch-a", time.Millisecond, slog.Default())
	if err != nil {
		t.Fatalf("expected clean cancellation, got %v", err)
	}
}
