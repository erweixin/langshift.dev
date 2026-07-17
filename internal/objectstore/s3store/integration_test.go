//go:build integration

package s3store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestS3CompatiblePurgeRemovesAllVersionsAndDeleteMarkers(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	accessKey := os.Getenv("S3_TEST_ACCESS_KEY")
	secretKey := os.Getenv("S3_TEST_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("S3_TEST_ENDPOINT, S3_TEST_ACCESS_KEY and S3_TEST_SECRET_KEY are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	bucket := fmt.Sprintf("lites-purge-it-%d", time.Now().UnixNano())
	if _, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	}()
	if _, err = client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(bucket), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	key := "restricted/tenant/account/private"
	for index := 0; index < 3; index++ {
		body := []byte(fmt.Sprintf("private-version-%d", index))
		if _, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body)))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		t.Fatal(err)
	}
	store := Store{Client: client, Bucket: bucket, Prefix: "restricted", MaxBytes: 1 << 20, ServerSideEncryption: types.ServerSideEncryptionAes256, RequireDigestMetadata: true}
	receipt, err := store.Purge(ctx, "s3://"+bucket+"/"+key)
	if err != nil || receipt.VersionsDeleted != 4 || len(receipt.Checksum) != 64 {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	remaining, err := store.listExactVersions(ctx, client, key)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
}

func TestS3CompatibleImmutableRoundTrip(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	accessKey := os.Getenv("S3_TEST_ACCESS_KEY")
	secretKey := os.Getenv("S3_TEST_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("S3_TEST_ENDPOINT, S3_TEST_ACCESS_KEY and S3_TEST_SECRET_KEY are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	bucket := fmt.Sprintf("lites-it-%d", time.Now().UnixNano())
	if _, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	}()
	store := Store{Client: client, Bucket: bucket, Prefix: "restricted", MaxBytes: 1 << 20, ServerSideEncryption: types.ServerSideEncryptionAes256, RequireDigestMetadata: true}
	if err = store.Ready(ctx); err != nil {
		t.Fatalf("object store is not ready: %v", err)
	}
	ref, err := store.Put(ctx, "tenant/event/id/hash", []byte("encrypted envelope"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("restricted/tenant/event/id/hash")})
	}()
	actual, err := store.Get(ctx, ref)
	if err != nil || string(actual) != "encrypted envelope" {
		t.Fatalf("body=%q err=%v", actual, err)
	}
	if _, err = store.Put(ctx, "tenant/event/id/hash", []byte("encrypted envelope")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Put(ctx, "tenant/event/id/hash", []byte("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected immutable conflict, got %v", err)
	}
}
