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
	"mime"
	"net/url"
	"path"
	"regexp"
	"sort"
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
	ErrNotFound      = errors.New("s3 object was not found")
	ErrTooLarge      = errors.New("s3 object exceeds configured size limit")
)

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	// Colons are permitted inside a path segment because durable event object
	// IDs use the stable "aggregate-id:event-stage" form. They are safe in an
	// S3 URL path and remain subject to the traversal and canonical-path checks
	// in validObjectKey.
	objectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	digestRE      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

type versionAPI interface {
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
}

type PurgeReceipt struct {
	VersionsDeleted int
	Checksum        string
}

type VersionedObject struct {
	Reference, VersionID, ContentHash, MediaType, ScanResultHash string
	ByteSize                                                     int64
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

// PutVersioned writes a scanned commercial Artifact into a bucket whose S3
// versioning policy is enabled. Unlike encrypted payload storage, Artifact
// provenance requires the provider-issued immutable VersionID; an unversioned
// success is rejected rather than silently weakening the revision manifest.
func (store Store) PutVersioned(ctx context.Context, objectKey, mediaType string, contents []byte) (VersionedObject, error) {
	return store.putVersioned(ctx, objectKey, mediaType, contents, "")
}

// PutScannedVersioned binds the trusted malware-scan receipt to the immutable
// object metadata so an outcome-unknown write can later be reconciled without
// trusting a model or repeating the external effect.
func (store Store) PutScannedVersioned(ctx context.Context, objectKey, mediaType string, contents []byte, scanResultHash string) (VersionedObject, error) {
	if !digestRE.MatchString(scanResultHash) {
		return VersionedObject{}, ErrConfiguration
	}
	return store.putVersioned(ctx, objectKey, mediaType, contents, scanResultHash)
}

func (store Store) putVersioned(ctx context.Context, objectKey, mediaType string, contents []byte, scanResultHash string) (VersionedObject, error) {
	parsedType, parameters, mediaErr := mime.ParseMediaType(mediaType)
	if err := store.validate(); err != nil || !validObjectKey(objectKey) || len(contents) == 0 || mediaErr != nil || parsedType != mediaType || len(parameters) != 0 {
		return VersionedObject{}, ErrConfiguration
	}
	if int64(len(contents)) > store.MaxBytes {
		return VersionedObject{}, ErrTooLarge
	}
	key := store.key(objectKey)
	ref := store.reference(key)
	digest := sha256.Sum256(contents)
	digestHex := hex.EncodeToString(digest[:])
	digestBase64 := base64.StdEncoding.EncodeToString(digest[:])
	metadata := map[string]string{"lites-sha256": digestHex}
	if scanResultHash != "" {
		metadata["lites-scan-result-sha256"] = scanResultHash
	}
	input := &s3.PutObjectInput{Bucket: aws.String(store.Bucket), Key: aws.String(key), Body: bytes.NewReader(contents), ContentLength: aws.Int64(int64(len(contents))), ContentType: aws.String(mediaType), CacheControl: aws.String("private, no-store"), IfNoneMatch: aws.String("*"), ChecksumAlgorithm: types.ChecksumAlgorithmSha256, ChecksumSHA256: aws.String(digestBase64), Metadata: metadata, ServerSideEncryption: store.ServerSideEncryption}
	if store.ServerSideEncryption == types.ServerSideEncryptionAwsKms {
		input.SSEKMSKeyId = aws.String(store.KMSKeyID)
		input.BucketKeyEnabled = aws.Bool(true)
	}
	output, err := store.Client.PutObject(ctx, input)
	if err == nil {
		if output == nil || aws.ToString(output.VersionId) == "" {
			return VersionedObject{}, ErrIntegrity
		}
		return VersionedObject{Reference: ref, VersionID: aws.ToString(output.VersionId), ContentHash: digestHex, MediaType: mediaType, ByteSize: int64(len(contents)), ScanResultHash: scanResultHash}, nil
	}
	if !isConditionalConflict(err) {
		return VersionedObject{}, err
	}
	existing, getErr := store.Get(ctx, ref)
	if getErr != nil {
		return VersionedObject{}, getErr
	}
	if !bytes.Equal(existing, contents) {
		return VersionedObject{}, ErrConflict
	}
	head, headErr := store.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(store.Bucket), Key: aws.String(key)})
	if headErr != nil {
		return VersionedObject{}, headErr
	}
	if head == nil || aws.ToString(head.VersionId) == "" || aws.ToString(head.ContentType) != mediaType || aws.ToInt64(head.ContentLength) != int64(len(contents)) || !strings.EqualFold(head.Metadata["lites-sha256"], digestHex) || !strings.EqualFold(head.Metadata["lites-scan-result-sha256"], scanResultHash) {
		return VersionedObject{}, ErrIntegrity
	}
	return VersionedObject{Reference: ref, VersionID: aws.ToString(head.VersionId), ContentHash: digestHex, MediaType: mediaType, ByteSize: int64(len(contents)), ScanResultHash: scanResultHash}, nil
}

// InspectScannedVersioned performs a strongly consistent metadata lookup for
// the exact key that a provider request was authorized to create. It never
// accepts an unversioned or partially bound object as evidence of success.
func (store Store) InspectScannedVersioned(ctx context.Context, objectKey, mediaType, contentHash string, byteSize int64) (VersionedObject, error) {
	parsedType, parameters, mediaErr := mime.ParseMediaType(mediaType)
	if err := store.validate(); err != nil || !validObjectKey(objectKey) || mediaErr != nil || parsedType != mediaType || len(parameters) != 0 || !digestRE.MatchString(contentHash) || byteSize < 1 || byteSize > store.MaxBytes {
		return VersionedObject{}, ErrConfiguration
	}
	key := store.key(objectKey)
	head, err := store.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(store.Bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return VersionedObject{}, ErrNotFound
		}
		return VersionedObject{}, err
	}
	scanHash := ""
	if head != nil {
		scanHash = strings.ToLower(head.Metadata["lites-scan-result-sha256"])
	}
	if head == nil || aws.ToString(head.VersionId) == "" || aws.ToString(head.ContentType) != mediaType || aws.ToInt64(head.ContentLength) != byteSize || !strings.EqualFold(head.Metadata["lites-sha256"], contentHash) || !digestRE.MatchString(scanHash) {
		return VersionedObject{}, ErrIntegrity
	}
	return VersionedObject{Reference: store.reference(key), VersionID: aws.ToString(head.VersionId), ContentHash: strings.ToLower(contentHash), MediaType: mediaType, ByteSize: byteSize, ScanResultHash: scanHash}, nil
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

// Delete removes an object only after parsing it back through this store's
// bucket and prefix boundary. S3 deletion is idempotent, which lets erasure
// workers safely retry an unknown result without widening their authority.
func (store Store) Delete(ctx context.Context, ref string) error {
	if err := store.validate(); err != nil {
		return err
	}
	key, err := store.parseReference(ref)
	if err != nil {
		return err
	}
	output, err := store.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(store.Bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	if output == nil {
		return ErrIntegrity
	}
	return nil
}

// Purge removes every retained version and delete marker for one exact object
// key, then performs a second complete listing before issuing a receipt. It is
// the only deletion primitive suitable for data-subject erasure on a versioned
// bucket.
func (store Store) Purge(ctx context.Context, ref string) (PurgeReceipt, error) {
	if err := store.validate(); err != nil {
		return PurgeReceipt{}, err
	}
	key, err := store.parseReference(ref)
	if err != nil {
		return PurgeReceipt{}, err
	}
	versions, ok := store.Client.(versionAPI)
	if !ok {
		return PurgeReceipt{}, ErrConfiguration
	}
	entries, err := store.listExactVersions(ctx, versions, key)
	if err != nil {
		return PurgeReceipt{}, err
	}
	sort.Strings(entries)
	for _, entry := range entries {
		separator := strings.IndexByte(entry, 0)
		if separator < 1 || separator == len(entry)-1 {
			return PurgeReceipt{}, ErrIntegrity
		}
		if _, err = store.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(store.Bucket), Key: aws.String(key), VersionId: aws.String(entry[separator+1:])}); err != nil {
			return PurgeReceipt{}, err
		}
	}
	remaining, err := store.listExactVersions(ctx, versions, key)
	if err != nil {
		return PurgeReceipt{}, err
	}
	if len(remaining) != 0 {
		return PurgeReceipt{}, ErrIntegrity
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("lites-s3-version-purge-v1\x00"))
	_, _ = digest.Write([]byte(store.Bucket + "\x00" + key + "\x00"))
	for _, entry := range entries {
		_, _ = digest.Write([]byte(entry + "\x00"))
	}
	return PurgeReceipt{VersionsDeleted: len(entries), Checksum: hex.EncodeToString(digest.Sum(nil))}, nil
}

func (store Store) listExactVersions(ctx context.Context, api versionAPI, key string) ([]string, error) {
	entries := make([]string, 0)
	input := &s3.ListObjectVersionsInput{Bucket: aws.String(store.Bucket), Prefix: aws.String(key), MaxKeys: aws.Int32(1000)}
	for {
		output, err := api.ListObjectVersions(ctx, input)
		if err != nil {
			return nil, err
		}
		if output == nil {
			return nil, ErrIntegrity
		}
		for _, version := range output.Versions {
			if aws.ToString(version.Key) == key && aws.ToString(version.VersionId) != "" {
				entries = append(entries, "version\x00"+aws.ToString(version.VersionId))
			}
		}
		for _, marker := range output.DeleteMarkers {
			if aws.ToString(marker.Key) == key && aws.ToString(marker.VersionId) != "" {
				entries = append(entries, "marker\x00"+aws.ToString(marker.VersionId))
			}
		}
		if !aws.ToBool(output.IsTruncated) {
			return entries, nil
		}
		if aws.ToString(output.NextKeyMarker) == "" || aws.ToString(output.NextVersionIdMarker) == "" {
			return nil, ErrIntegrity
		}
		input.KeyMarker = output.NextKeyMarker
		input.VersionIdMarker = output.NextVersionIdMarker
	}
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

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NoSuchObject"
}

func (store Store) String() string { return fmt.Sprintf("s3://%s/%s", store.Bucket, store.Prefix) }
