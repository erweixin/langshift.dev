package main

import (
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type config struct {
	databaseURL, databaseURLFile                                                                                                     string
	epochURL, epochTokenFile, epochCAFile, epochCertFile, epochKeyFile, epochTLSName                                                 string
	capabilityKeyringFile, capabilityIssuer, capabilityAudience                                                                      string
	idKeyFile, tokenPepperFile, hostControlTokenFile, ownershipKeyFile                                                               string
	hostID, poolKey, architecture, availabilityZone                                                                                  string
	kernelCatalogHash, rootfsCatalogHash, scratchDigest                                                                              string
	capacityVCPU, capacityMemoryMiB, capacityDiskMiB, capacitySessions                                                               int
	jailerPath, jailerDigest, firecrackerPath, firecrackerDigest, chrootBaseDir, parentCgroup, networkNamespace                      string
	jailerUID, jailerGID                                                                                                             uint32
	cpuQuotaMicros, cpuPeriodMicros, memoryMaxBytes, pidsMax, fileSizeMaxBytes, noFileMax                                            int64
	kernelPath, kernelDigest, rootfsPath, rootfsDigest, scratchPath                                                                  string
	maximumKernelBytes, maximumRootFSBytes, maximumScratchBytes                                                                      int64
	s3Region, s3Endpoint, payloadBucket, payloadPrefix, s3KMSKeyID                                                                   string
	s3PathStyle                                                                                                                      bool
	s3Encryption                                                                                                                     types.ServerSideEncryption
	vaultAddress, vaultNamespace, vaultMount, vaultTokenFile, vaultCAFile, vaultCertFile, vaultKeyFile, vaultTLSName, vaultKeyPrefix string
	listenAddress, healthAddress, serverCertFile, serverKeyFile, serverClientCAFile, serverAllowedClientSPIFFEID                     string
	environment, serviceVersion, region, otlpEndpoint, otlpCAFile, otlpCertFile, otlpKeyFile, otlpTLSName, otlpBearerTokenFile       string
	heartbeatInterval, heartbeatTTL, recoveryInterval, shutdownTimeout                                                               time.Duration
	traceSampleRatio                                                                                                                 float64
	allowInsecureDevelopment                                                                                                         bool
}

func loadConfig() (config, error) {
	allow, err := optionalBool("ALLOW_INSECURE_DEVELOPMENT", false)
	if err != nil {
		return config{}, err
	}
	value := config{
		databaseURL: os.Getenv("DATABASE_URL"), databaseURLFile: os.Getenv("DATABASE_URL_FILE"),
		epochURL: os.Getenv("STORE_EPOCH_AUTHORITY_URL"), epochTokenFile: os.Getenv("STORE_EPOCH_TOKEN_FILE"), epochCAFile: os.Getenv("STORE_EPOCH_CA_FILE"), epochCertFile: os.Getenv("STORE_EPOCH_CLIENT_CERT_FILE"), epochKeyFile: os.Getenv("STORE_EPOCH_CLIENT_KEY_FILE"), epochTLSName: os.Getenv("STORE_EPOCH_TLS_SERVER_NAME"),
		capabilityKeyringFile: os.Getenv("RUNTIME_CAPABILITY_KEYRING_FILE"), capabilityIssuer: env("RUNTIME_CAPABILITY_ISSUER", "event-service"), capabilityAudience: env("RUNTIME_CAPABILITY_AUDIENCE", "runtime-manager"),
		idKeyFile: os.Getenv("RUNTIME_ID_KEY_FILE"), tokenPepperFile: os.Getenv("RUNTIME_TOKEN_PEPPER_FILE"), hostControlTokenFile: os.Getenv("RUNTIME_HOST_CONTROL_TOKEN_FILE"), ownershipKeyFile: os.Getenv("RUNTIME_OWNERSHIP_KEY_FILE"),
		hostID: os.Getenv("RUNTIME_HOST_ID"), poolKey: os.Getenv("RUNTIME_POOL_KEY"), architecture: os.Getenv("RUNTIME_ARCHITECTURE"), availabilityZone: os.Getenv("RUNTIME_AVAILABILITY_ZONE"),
		kernelCatalogHash: os.Getenv("RUNTIME_KERNEL_CATALOG_HASH"), rootfsCatalogHash: os.Getenv("RUNTIME_ROOTFS_CATALOG_HASH"), scratchDigest: os.Getenv("RUNTIME_SCRATCH_DIGEST"),
		jailerPath: os.Getenv("JAILER_PATH"), jailerDigest: os.Getenv("JAILER_DIGEST"), firecrackerPath: os.Getenv("FIRECRACKER_PATH"), firecrackerDigest: os.Getenv("FIRECRACKER_BINARY_DIGEST"), chrootBaseDir: os.Getenv("FIRECRACKER_CHROOT_BASE_DIR"), parentCgroup: os.Getenv("FIRECRACKER_PARENT_CGROUP"), networkNamespace: os.Getenv("FIRECRACKER_NETWORK_NAMESPACE"),
		kernelPath: os.Getenv("FIRECRACKER_KERNEL_PATH"), kernelDigest: os.Getenv("FIRECRACKER_KERNEL_DIGEST"), rootfsPath: os.Getenv("FIRECRACKER_ROOTFS_PATH"), rootfsDigest: os.Getenv("FIRECRACKER_ROOTFS_DIGEST"), scratchPath: os.Getenv("FIRECRACKER_SCRATCH_PATH"),
		s3Region: os.Getenv("S3_REGION"), s3Endpoint: os.Getenv("S3_ENDPOINT"), payloadBucket: os.Getenv("S3_PAYLOAD_BUCKET"), payloadPrefix: env("S3_PAYLOAD_PREFIX", "restricted"), s3KMSKeyID: os.Getenv("S3_KMS_KEY_ID"), s3Encryption: types.ServerSideEncryption(env("S3_SERVER_SIDE_ENCRYPTION", string(types.ServerSideEncryptionAes256))),
		vaultAddress: os.Getenv("VAULT_ADDR"), vaultNamespace: os.Getenv("VAULT_NAMESPACE"), vaultMount: env("VAULT_KV_MOUNT", "secret"), vaultTokenFile: os.Getenv("VAULT_TOKEN_FILE"), vaultCAFile: os.Getenv("VAULT_CACERT"), vaultCertFile: os.Getenv("VAULT_CLIENT_CERT_FILE"), vaultKeyFile: os.Getenv("VAULT_CLIENT_KEY_FILE"), vaultTLSName: os.Getenv("VAULT_TLS_SERVER_NAME"), vaultKeyPrefix: env("VAULT_PAYLOAD_KEY_PREFIX", "lites/payload-keys"),
		listenAddress: env("LISTEN_ADDRESS", ":8443"), healthAddress: env("HEALTH_ADDRESS", "127.0.0.1:8085"), serverCertFile: os.Getenv("SERVER_TLS_CERT_FILE"), serverKeyFile: os.Getenv("SERVER_TLS_KEY_FILE"), serverClientCAFile: os.Getenv("SERVER_CLIENT_CA_FILE"), serverAllowedClientSPIFFEID: os.Getenv("SERVER_ALLOWED_CLIENT_SPIFFE_ID"),
		environment: os.Getenv("LITES_ENVIRONMENT"), serviceVersion: os.Getenv("LITES_VERSION"), region: os.Getenv("LITES_REGION"), otlpEndpoint: os.Getenv("OTLP_GRPC_ENDPOINT"), otlpCAFile: os.Getenv("OTLP_ROOT_CA_FILE"), otlpCertFile: os.Getenv("OTLP_CLIENT_CERT_FILE"), otlpKeyFile: os.Getenv("OTLP_CLIENT_KEY_FILE"), otlpTLSName: os.Getenv("OTLP_TLS_SERVER_NAME"), otlpBearerTokenFile: os.Getenv("OTLP_BEARER_TOKEN_FILE"),
		allowInsecureDevelopment: allow,
	}
	if value.databaseURLFile != "" {
		if value.databaseURL != "" {
			return config{}, errors.New("DATABASE_URL and DATABASE_URL_FILE are mutually exclusive")
		}
		value.databaseURL, err = readSecret(value.databaseURLFile, 8192)
		if err != nil {
			return config{}, errors.New("DATABASE_URL_FILE is unreadable")
		}
	}
	ints := []struct {
		name     string
		target   *int
		fallback int
	}{
		{"RUNTIME_CAPACITY_VCPU", &value.capacityVCPU, 32}, {"RUNTIME_CAPACITY_MEMORY_MIB", &value.capacityMemoryMiB, 65536}, {"RUNTIME_CAPACITY_DISK_MIB", &value.capacityDiskMiB, 1048576}, {"RUNTIME_CAPACITY_SESSIONS", &value.capacitySessions, 64},
	}
	for _, item := range ints {
		if *item.target, err = optionalInt(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	uints := []struct {
		name     string
		target   *uint32
		fallback uint32
	}{{"FIRECRACKER_UID", &value.jailerUID, 1000}, {"FIRECRACKER_GID", &value.jailerGID, 1000}}
	for _, item := range uints {
		if *item.target, err = optionalUint32(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	int64s := []struct {
		name     string
		target   *int64
		fallback int64
	}{
		{"FIRECRACKER_CPU_QUOTA_MICROS", &value.cpuQuotaMicros, 200000}, {"FIRECRACKER_CPU_PERIOD_MICROS", &value.cpuPeriodMicros, 100000}, {"FIRECRACKER_MEMORY_MAX_BYTES", &value.memoryMaxBytes, 2 << 30}, {"FIRECRACKER_PIDS_MAX", &value.pidsMax, 512}, {"FIRECRACKER_FILE_SIZE_MAX_BYTES", &value.fileSizeMaxBytes, 16 << 30}, {"FIRECRACKER_NOFILE_MAX", &value.noFileMax, 4096},
		{"FIRECRACKER_MAX_KERNEL_BYTES", &value.maximumKernelBytes, 256 << 20}, {"FIRECRACKER_MAX_ROOTFS_BYTES", &value.maximumRootFSBytes, 16 << 30}, {"FIRECRACKER_MAX_SCRATCH_BYTES", &value.maximumScratchBytes, 1 << 30},
	}
	for _, item := range int64s {
		if *item.target, err = optionalInt64(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	if value.s3PathStyle, err = optionalBool("S3_PATH_STYLE", false); err != nil {
		return config{}, err
	}
	durations := []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{
		{"RUNTIME_HEARTBEAT_INTERVAL", &value.heartbeatInterval, 10 * time.Second}, {"RUNTIME_HEARTBEAT_TTL", &value.heartbeatTTL, 30 * time.Second}, {"RUNTIME_RECOVERY_INTERVAL", &value.recoveryInterval, 2 * time.Second}, {"RUNTIME_SHUTDOWN_TIMEOUT", &value.shutdownTimeout, 45 * time.Second},
	}
	for _, item := range durations {
		if *item.target, err = optionalDuration(item.name, item.fallback); err != nil {
			return config{}, err
		}
	}
	if value.traceSampleRatio, err = optionalFloat("TRACE_SAMPLE_RATIO", 0.1); err != nil {
		return config{}, err
	}
	return value, value.validate()
}

func (value config) validate() error {
	required := []string{value.databaseURL, value.epochURL, value.capabilityKeyringFile, value.idKeyFile, value.tokenPepperFile, value.hostControlTokenFile, value.ownershipKeyFile, value.hostID, value.poolKey, value.architecture, value.availabilityZone, value.kernelCatalogHash, value.rootfsCatalogHash, value.scratchDigest, value.jailerPath, value.jailerDigest, value.firecrackerPath, value.firecrackerDigest, value.chrootBaseDir, value.parentCgroup, value.kernelPath, value.kernelDigest, value.rootfsPath, value.rootfsDigest, value.scratchPath, value.s3Region, value.payloadBucket, value.vaultAddress, value.vaultMount, value.vaultKeyPrefix, value.listenAddress, value.healthAddress, value.environment, value.serviceVersion, value.region}
	for _, item := range required {
		if item == "" {
			return errors.New("required runtime host configuration is missing")
		}
	}
	if value.capacityVCPU < 1 || value.capacityMemoryMiB < 128 || value.capacityDiskMiB < 64 || value.capacitySessions < 1 || value.jailerUID == 0 || value.jailerGID == 0 || value.heartbeatInterval <= 0 || value.heartbeatTTL <= value.heartbeatInterval || value.recoveryInterval <= 0 || value.shutdownTimeout <= 0 || value.traceSampleRatio < 0 || value.traceSampleRatio > 1 || (value.serverCertFile == "") != (value.serverKeyFile == "") || (value.epochCertFile == "") != (value.epochKeyFile == "") || (value.vaultCertFile == "") != (value.vaultKeyFile == "") || (value.otlpCertFile == "") != (value.otlpKeyFile == "") {
		return errors.New("runtime host configuration is invalid")
	}
	digestPattern := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for _, digest := range []string{value.kernelCatalogHash, value.rootfsCatalogHash, value.scratchDigest, value.jailerDigest, value.firecrackerDigest, value.kernelDigest, value.rootfsDigest} {
		if !digestPattern.MatchString(digest) {
			return errors.New("runtime host digest is invalid")
		}
	}
	if value.s3Encryption == types.ServerSideEncryptionAwsKms && value.s3KMSKeyID == "" || value.s3Encryption != types.ServerSideEncryptionAwsKms && value.s3Encryption != types.ServerSideEncryptionAes256 {
		return errors.New("invalid S3 encryption")
	}
	if !value.allowInsecureDevelopment && (value.databaseURLFile == "" || value.epochTokenFile == "" && value.epochCertFile == "" || value.epochCAFile == "" || value.serverCertFile == "" || value.serverClientCAFile == "" || value.serverAllowedClientSPIFFEID == "" || value.vaultTokenFile == "" && value.vaultCertFile == "" || value.vaultCAFile == "" || value.otlpEndpoint == "" || value.otlpBearerTokenFile == "" && value.otlpCertFile == "") {
		return errors.New("production runtime host requires file-backed mTLS credentials")
	}
	parsed, err := url.Parse(value.epochURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (!value.allowInsecureDevelopment && parsed.Scheme != "https") || value.allowInsecureDevelopment && parsed.Scheme == "http" && !loopback(parsed.Hostname()) {
		return errors.New("invalid store epoch endpoint")
	}
	if value.serverAllowedClientSPIFFEID != "" {
		identity, identityErr := url.Parse(value.serverAllowedClientSPIFFEID)
		if identityErr != nil || identity.Scheme != "spiffe" || identity.Host == "" || identity.User != nil || identity.RawQuery != "" || identity.Fragment != "" {
			return errors.New("invalid runtime control SPIFFE identity")
		}
	}
	return nil
}

func readSecret(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	value := strings.TrimSpace(string(contents))
	if err != nil || int64(len(contents)) > limit || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("invalid secret")
	}
	return value, nil
}
func readBase64Key(path string) ([]byte, error) {
	value, err := readSecret(path, 256)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("invalid 32-byte key")
	}
	return decoded, nil
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func optionalBool(name string, fallback bool) (bool, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return strconv.ParseBool(value)
}
func optionalInt(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return strconv.Atoi(value)
}
func optionalInt64(name string, fallback int64) (int64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
func optionalUint32(name string, fallback uint32) (uint32, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	return uint32(parsed), err
}
func optionalDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return time.ParseDuration(value)
}
func optionalFloat(name string, fallback float64) (float64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	return strconv.ParseFloat(value, 64)
}
func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
