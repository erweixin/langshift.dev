package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestClientPinsVersionAndConfiguresMachineInSafeOrder(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	server := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		if request.URL.Path == "/version" {
			return jsonResponse(http.StatusOK, `{"firecracker_version":"1.15.1"}`), nil
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode %s: %v", request.URL.Path, err)
		}
		bodies = append(bodies, body)
		return jsonResponse(http.StatusNoContent, ""), nil
	})}

	client := Client{HTTPClient: server}
	if err := client.ConfigureAndStart(context.Background(), validSpec()); err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"GET /version", "PUT /machine-config", "PUT /boot-source", "PUT /drives/rootfs", "PUT /drives/scratch", "PUT /vsock", "PUT /actions"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v", paths)
	}
	if bodies[0]["smt"] != false || bodies[0]["track_dirty_pages"] != false || bodies[1]["boot_args"] != bootArguments || bodies[2]["is_read_only"] != true || bodies[3]["is_read_only"] != false || bodies[4]["guest_cid"] != float64(42) || bodies[5]["action_type"] != "InstanceStart" {
		t.Fatalf("unsafe or incomplete configuration: %#v", bodies)
	}
	if _, ok := bodies[0]["network_interfaces"]; ok {
		t.Fatal("default machine unexpectedly contains a network interface")
	}
}

func TestClientRejectsVersionDriftBeforeConfiguration(t *testing.T) {
	server := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"firecracker_version":"1.16.0"}`), nil
	})}
	err := (Client{HTTPClient: server}).ConfigureAndStart(context.Background(), validSpec())
	if !errors.Is(err, ErrIncompatibleVMM) {
		t.Fatalf("ConfigureAndStart() = %v", err)
	}
}

func TestClientRejectsAmbiguousOrOversizedVersionResponse(t *testing.T) {
	tests := []string{
		`{"firecracker_version":"1.15.1"}{"firecracker_version":"1.15.1"}`,
		`{"firecracker_version":"1.15.1","unexpected":true}`,
		`{"firecracker_version":"1.15.1"}` + strings.Repeat(" ", maximumAPIErrorBytes),
	}
	for _, body := range tests {
		server := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, body), nil
		})}
		if _, err := (Client{HTTPClient: server}).Version(context.Background()); !errors.Is(err, ErrAPI) {
			t.Fatalf("Version() accepted ambiguous response: %v", err)
		}
	}
}

func TestClientFailsClosedOnNon204Response(t *testing.T) {
	calls := 0
	server := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Path == "/version" {
			return jsonResponse(http.StatusOK, `{"firecracker_version":"1.15.1"}`), nil
		}
		return jsonResponse(http.StatusBadRequest, "invalid machine\nconfiguration"), nil
	})}
	err := (Client{HTTPClient: server}).ConfigureAndStart(context.Background(), validSpec())
	if !errors.Is(err, ErrAPI) || calls != 2 {
		t.Fatalf("ConfigureAndStart() = %v, calls = %d", err, calls)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
