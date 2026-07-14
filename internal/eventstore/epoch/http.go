// Package epoch reads the authoritative EventStore generation from a fault
// domain independent of PostgreSQL PITR.
package epoch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var ErrAuthorityUnavailable = errors.New("store epoch authority is unavailable")

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type HTTPAuthority struct {
	Endpoint              string
	BearerToken           string
	Client                *http.Client
	AllowInsecureLoopback bool
}

func (authority HTTPAuthority) CurrentStoreEpoch(ctx context.Context) (string, error) {
	endpoint, err := url.Parse(authority.Endpoint)
	if err != nil || endpoint.Path == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "https" && !(authority.AllowInsecureLoopback && endpoint.Scheme == "http" && isLoopback(endpoint.Hostname()))) || authority.Client == nil {
		return "", ErrAuthorityUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", ErrAuthorityUnavailable
	}
	request.Header.Set("Accept", "application/json")
	if authority.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+authority.BearerToken)
	}
	response, err := authority.Client.Do(request)
	if err != nil {
		return "", ErrAuthorityUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", ErrAuthorityUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return "", ErrAuthorityUnavailable
	}
	var value struct {
		StoreEpoch string `json:"store_epoch"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil || !uuidPattern.MatchString(value.StoreEpoch) {
		return "", ErrAuthorityUnavailable
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", ErrAuthorityUnavailable
	}
	return value.StoreEpoch, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (authority HTTPAuthority) String() string {
	endpoint, err := url.Parse(authority.Endpoint)
	if err != nil {
		return "epoch-authority(invalid)"
	}
	return fmt.Sprintf("epoch-authority(%s://%s)", endpoint.Scheme, endpoint.Host)
}
