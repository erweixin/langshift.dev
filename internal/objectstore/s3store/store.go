// Package s3store implements immutable, bounded S3-compatible object storage.
package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var (
	ErrConfiguration = errors.New("s3 object store configuration is invalid")
	ErrReference     = errors.New("s3 object reference is invalid")
	ErrConflict      = errors.New("immutable s3 object already exists with different content")
	ErrIntegrity     = errors.New("s3 object integrity verification failed")
	ErrTooLarge      = errors.New("s3 object exceeds configured size limit")
)

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	objectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

type API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

type Store struct {
	Client                API
	Bucket                string
	Prefix                string
	MaxBytes              int64
	ServerSideEncryption  types.ServerSideEncryption
	KMSKeyID              string
	RequireDigestMetadata bool
}

func (store Store) Ready(ctx context.Context) error {
	if err := store.validate(); err != nil {
		return err
	}
	output, err := store.Client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(store.Bucket)})
	if err != nil || output == nil {
		if err != nil {
			return err
		}
		return ErrIntegrity
	}
	return nil
}

func (store Store) Put(ctx context.Context, objectKey string, contents []byte) (string, error) {
	if err := store.validate(); err != nil || !validObjectKey(objectKey) || len(contents) == 0 {
		return "", ErrConfiguration
	}
	if int64(len(contents)) > store.MaxBytes {
		return "", ErrTooLarge
	}
	key := store.key(objectKey)
	ref := store.reference(key)
	digest := sha256.Sum256(contents)
	digestHex := hex.EncodeToString(digest[:])
	digestBase64 := base64.StdEncoding.EncodeToString(digest[:])
	input := &s3.PutObjectInput{Bucket: aws.String(store.Bucket), Key: aws.String(key), Body: bytes.NewReader(contents), ContentLength: aws.Int64(int64(len(contents))), ContentType: aws.String("application/octet-stream"), CacheControl: aws.String("private, no-store"), IfNoneMatch: aws.String("*"), ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: aws.String(digestBase64), Metadata: map[string]string{"lites-sha256": digestHex}, ServerSideEncryption: store.ServerSideEncryption}
	if store.ServerSideEncryption == types.ServerSideEncryptionAwsKms {
		input.SSEKMSKeyId = aws.String(store.KMSKeyID)
		input.BucketKeyEnabled = aws.Bool(true)
	}
	output, err := store.Client.PutObject(ctx, input)
	if err == nil {
		if output == nil {
			return "", ErrIntegrity
		}
		return ref, nil
	}
	if !isConditionalConflict(err) {
		return "", err
	}
	existing, getErr := store.Get(ctx, ref)
	if getErr != nil {
		return "", getErr
	}
	if !bytes.Equal(existing, contents) {
		return "", ErrConflict
	}
	return ref, nil
}

func (store Store) Get(ctx context.Context, ref string) ([]byte, error) {
	if err := store.validate(); err != nil {
		return nil, err
	}
	key, err := store.parseReference(ref)
	if err != nil {
		return nil, err
	}
	output, err := store.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(store.Bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled})
	if err != nil {
		return nil, err
	}
	if output == nil || output.Body == nil || (output.ContentLength != nil && (*output.ContentLength < 0 || *output.ContentLength > store.MaxBytes)) {
		if output != nil && output.Body != nil {
			_ = output.Body.Close()
		}
		return nil, ErrTooLarge
	}
	contents, readErr := io.ReadAll(io.LimitReader(output.Body, store.MaxBytes+1))
	closeErr := output.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(contents)) > store.MaxBytes {
		return nil, ErrTooLarge
	}
	digest := sha256.Sum256(contents)
	digestHex := hex.EncodeToString(digest[:])
	metadataDigest := output.Metadata["lites-sha256"]
	if store.RequireDigestMetadata && metadataDigest == "" {
		return nil, ErrIntegrity
	}
	if metadataDigest != "" && !strings.EqualFold(metadataDigest, digestHex) {
		return nil, ErrIntegrity
	}
	if output.ChecksumSHA256 != nil && *output.ChecksumSHA256 != "" && *output.ChecksumSHA256 != base64.StdEncoding.EncodeToString(digest[:]) {
		return nil, ErrIntegrity
	}
	return contents, nil
}

func (store Store) validate() error {
	if store.Client == nil || !validBucket(store.Bucket) || store.MaxBytes < 1 || store.MaxBytes > 5<<30 || !validPrefix(store.Prefix) {
		return ErrConfiguration
	}
	switch store.ServerSideEncryption {
	case types.ServerSideEncryptionAes256:
		if store.KMSKeyID != "" {
			return ErrConfiguration
		}
	case types.ServerSideEncryptionAwsKms:
		if strings.TrimSpace(store.KMSKeyID) == "" {
			return ErrConfiguration
		}
	default:
		return ErrConfiguration
	}
	return nil
}

func (store Store) key(objectKey string) string {
	if store.Prefix == "" {
		return objectKey
	}
	return strings.TrimSuffix(store.Prefix, "/") + "/" + objectKey
}

func (store Store) reference(key string) string { return "s3://" + store.Bucket + "/" + key }

func (store Store) parseReference(ref string) (string, error) {
	parsed, err := url.Parse(ref)
	if err != nil || parsed.Scheme != "s3" || parsed.Host != store.Bucket || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", ErrReference
	}
	key := strings.TrimPrefix(parsed.Path, "/")
	prefix := strings.TrimSuffix(store.Prefix, "/")
	if !validObjectKey(key) || (prefix != "" && !strings.HasPrefix(key, prefix+"/")) {
		return "", ErrReference
	}
	return key, nil
}

func validBucket(value string) bool {
	return bucketPattern.MatchString(value) && !strings.Contains(value, "..") && !strings.Contains(value, ".-") && !strings.Contains(value, "-.")
}

func validPrefix(value string) bool {
	if value == "" {
		return true
	}
	return validObjectKey(strings.TrimSuffix(value, "/"))
}

func validObjectKey(value string) bool {
	if len(value) > 1024 || !objectPattern.MatchString(value) || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func isConditionalConflict(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "ConditionalRequestConflict"
}

func (store Store) String() string { return fmt.Sprintf("s3://%s/%s", store.Bucket, store.Prefix) }
