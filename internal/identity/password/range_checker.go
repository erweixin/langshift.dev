package password

import (
	"context"
	"crypto/sha1" // SHA-1 is the range corpus identifier, not a password KDF.
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maximumRangeResponseBytes int64 = 2 * 1024 * 1024

// RangeChecker implements a k-anonymity password compromise lookup. Only the
// first five hexadecimal characters of the SHA-1 corpus identifier leave the
// process; the complete digest and plaintext candidate remain local.
type RangeChecker struct {
	baseURL string
	client  *http.Client
}

// NewRangeChecker requires an explicit host allowlist so an operator-supplied
// endpoint cannot turn password screening into an arbitrary HTTPS request.
// Redirects are rejected even when the supplied client would normally follow
// them. Egress policy should additionally restrict the configured host.
func NewRangeChecker(rawBaseURL string, allowedHosts []string, client *http.Client) (*RangeChecker, error) {
	parsed, err := url.Parse(rawBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalidConfiguration
	}
	allowed := false
	for _, host := range allowedHosts {
		if strings.EqualFold(parsed.Hostname(), host) {
			allowed = true
			break
		}
	}
	if !allowed || client == nil || client.Timeout <= 0 {
		return nil, ErrInvalidConfiguration
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &RangeChecker{baseURL: strings.TrimRight(parsed.String(), "/"), client: &clientCopy}, nil
}

func (checker *RangeChecker) Compromised(ctx context.Context, value string) (bool, error) {
	if checker == nil || checker.client == nil || checker.baseURL == "" {
		return false, ErrInvalidConfiguration
	}
	digest := sha1.Sum([]byte(value))
	encoded := strings.ToUpper(hex.EncodeToString(digest[:]))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, checker.baseURL+"/"+encoded[:5], nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "text/plain")
	request.Header.Set("Add-Padding", "true")
	request.Header.Set("User-Agent", "Lites-Password-Screen/1")
	response, err := checker.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("password range service returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumRangeResponseBytes+1))
	if err != nil {
		return false, err
	}
	if int64(len(body)) > maximumRangeResponseBytes {
		return false, errors.New("password range response is too large")
	}
	targetSuffix := encoded[5:]
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 2 || len(parts[0]) != len(targetSuffix) || !validUpperHex(parts[0]) {
			return false, errors.New("password range response is malformed")
		}
		count, countErr := strconv.ParseUint(parts[1], 10, 64)
		if countErr != nil {
			return false, errors.New("password range response count is malformed")
		}
		if subtle.ConstantTimeCompare([]byte(parts[0]), []byte(targetSuffix)) == 1 && count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func validUpperHex(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'A' || character > 'F' {
				return false
			}
		}
	}
	return true
}

var _ CompromiseChecker = (*RangeChecker)(nil)
