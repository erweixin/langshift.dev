//go:build integration

package erasure

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	valkey "github.com/valkey-io/valkey-go"
)

func TestTaggedSubjectCachePutAndPurgeAreAtomicAndConfined(t *testing.T) {
	address := os.Getenv("VALKEY_TEST_ADDRESS")
	if address == "" {
		t.Skip("VALKEY_TEST_ADDRESS is required")
	}
	client, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{address}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cache := TaggedSubjectCache{Client: client, Namespace: "lites-erasure-it", ReceiptKey: bytes.Repeat([]byte{0x91}, 32)}
	keyA, err := cache.Put(ctx, "tenant-a", "user-a", "profile", []byte("private-a"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := cache.Put(ctx, "tenant-a", "user-a", "route", []byte("private-b"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := cache.Put(ctx, "tenant-a", "user-b", "profile", []byte("private-other"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Register(ctx, "tenant-a", "user-a", otherKey); err != ErrInvalid {
		t.Fatalf("cross-subject cache registration error=%v", err)
	}
	count, receipt, err := cache.PurgeSubject(ctx, "tenant-a", "user-a", "epoch-a")
	if err != nil || count != 2 || len(receipt) != 64 {
		t.Fatalf("count=%d receipt=%s err=%v", count, receipt, err)
	}
	values := client.DoMulti(ctx, client.B().Exists().Key(keyA).Build(), client.B().Exists().Key(keyB).Build(), client.B().Exists().Key(otherKey).Build(), client.B().Exists().Key(cache.tag("tenant-a", "user-a")).Build())
	want := []int64{0, 0, 1, 0}
	for index, result := range values {
		actual, resultErr := result.AsInt64()
		if resultErr != nil || actual != want[index] {
			t.Fatalf("exists[%d]=%d want=%d err=%v", index, actual, want[index], resultErr)
		}
	}
	count, receipt, err = cache.PurgeSubject(ctx, "tenant-a", "user-a", "epoch-b")
	if err != nil || count != 0 || len(receipt) != 64 {
		t.Fatalf("restore count=%d receipt=%s err=%v", count, receipt, err)
	}
	_ = client.Do(ctx, client.B().Del().Key(otherKey).Build()).Error()
}
