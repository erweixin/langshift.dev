package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const maximumAPIErrorBytes = 4096

type Client struct {
	SocketPath string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type machineConfiguration struct {
	MemoryMiB       int    `json:"mem_size_mib"`
	VCPUCount       int    `json:"vcpu_count"`
	SMT             bool   `json:"smt"`
	TrackDirtyPages bool   `json:"track_dirty_pages"`
	HugePages       string `json:"huge_pages"`
}

type bootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArguments   string `json:"boot_args"`
}

type drive struct {
	DriveID     string       `json:"drive_id"`
	Root        bool         `json:"is_root_device"`
	ReadOnly    bool         `json:"is_read_only"`
	Path        string       `json:"path_on_host"`
	CacheType   string       `json:"cache_type"`
	IOEngine    string       `json:"io_engine"`
	RateLimiter *rateLimiter `json:"rate_limiter,omitempty"`
}

type rateLimiter struct {
	Bandwidth  tokenBucket `json:"bandwidth"`
	Operations tokenBucket `json:"ops"`
}

type tokenBucket struct {
	Size         int64 `json:"size"`
	RefillMillis int64 `json:"refill_time"`
	Burst        int64 `json:"one_time_burst"`
}

type vsock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type action struct {
	Type string `json:"action_type"`
}

type versionResponse struct {
	Version string `json:"firecracker_version"`
}

func (client Client) ConfigureAndStart(ctx context.Context, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	version, err := client.Version(ctx)
	if err != nil {
		return err
	}
	if version != CompatibleVersion {
		return fmt.Errorf("%w: got %q", ErrIncompatibleVMM, version)
	}
	requests := []struct {
		path string
		body any
	}{
		{path: "/machine-config", body: machineConfiguration{MemoryMiB: spec.MemoryMiB, VCPUCount: spec.VCPUCount, SMT: false, TrackDirtyPages: false, HugePages: "None"}},
		{path: "/boot-source", body: bootSource{KernelImagePath: spec.KernelImagePath, BootArguments: bootArguments}},
		{path: "/drives/rootfs", body: drive{DriveID: "rootfs", Root: true, ReadOnly: true, Path: spec.RootDrivePath, CacheType: "Writeback", IOEngine: "Sync"}},
		{path: "/drives/scratch", body: drive{DriveID: "scratch", Root: false, ReadOnly: false, Path: spec.ScratchPath, CacheType: "Writeback", IOEngine: "Sync", RateLimiter: convertRateLimit(spec.ScratchLimit)}},
		{path: "/vsock", body: vsock{GuestCID: spec.GuestCID, UDSPath: spec.VSockPath}},
		{path: "/actions", body: action{Type: "InstanceStart"}},
	}
	for _, request := range requests {
		if err = client.put(ctx, request.path, request.body); err != nil {
			return err
		}
	}
	return nil
}

func (client Client) Version(ctx context.Context) (string, error) {
	httpClient, err := client.configuredHTTPClient()
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://firecracker/version", nil)
	if err != nil {
		return "", ErrAPI
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: version request", ErrAPI)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", apiStatusError(response)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximumAPIErrorBytes+1))
	if err != nil || len(contents) > maximumAPIErrorBytes {
		return "", fmt.Errorf("%w: invalid version response", ErrAPI)
	}
	var value versionResponse
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil || value.Version == "" {
		return "", fmt.Errorf("%w: invalid version response", ErrAPI)
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return "", fmt.Errorf("%w: invalid version response", ErrAPI)
	}
	return strings.TrimPrefix(value.Version, "v"), nil
}

func (client Client) put(ctx context.Context, path string, value any) error {
	httpClient, err := client.configuredHTTPClient()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 4096 {
		return fmt.Errorf("%w: encode PUT %s", ErrAPI, path)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://firecracker"+path, bytes.NewReader(encoded))
	if err != nil {
		return ErrAPI
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: PUT %s", ErrAPI, path)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return apiStatusError(response)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumAPIErrorBytes))
	return nil
}

func (client Client) configuredHTTPClient() (*http.Client, error) {
	if client.HTTPClient != nil {
		return client.HTTPClient, nil
	}
	if !jailedPath(client.SocketPath) || client.Timeout <= 0 || client.Timeout > 30*time.Second {
		return nil, ErrInvalidSpec
	}
	dialer := &net.Dialer{Timeout: client.Timeout, KeepAlive: -1}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		DisableKeepAlives:     true,
		MaxConnsPerHost:       1,
		ResponseHeaderTimeout: client.Timeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", client.SocketPath)
		},
	}
	return &http.Client{Transport: transport, Timeout: client.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func apiStatusError(response *http.Response) error {
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximumAPIErrorBytes+1))
	if err != nil {
		return fmt.Errorf("%w: status %d", ErrAPI, response.StatusCode)
	}
	contents = []byte(strings.Map(func(value rune) rune {
		if value < 0x20 || value == 0x7f {
			return -1
		}
		return value
	}, string(contents)))
	if len(contents) > maximumAPIErrorBytes {
		contents = contents[:maximumAPIErrorBytes]
	}
	message := strings.TrimSpace(string(contents))
	if message == "" {
		return fmt.Errorf("%w: status %d", ErrAPI, response.StatusCode)
	}
	return fmt.Errorf("%w: status %d: %s", ErrAPI, response.StatusCode, message)
}

func convertRateLimit(value RateLimit) *rateLimiter {
	convert := func(bucket TokenBucket) tokenBucket {
		return tokenBucket{Size: bucket.Size, RefillMillis: bucket.RefillMillis, Burst: bucket.Burst}
	}
	return &rateLimiter{Bandwidth: convert(value.Bandwidth), Operations: convert(value.Operations)}
}
