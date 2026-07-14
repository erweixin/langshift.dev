//go:build integration

package observability

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

type captureTraceServer struct {
	collectortrace.UnimplementedTraceServiceServer
	requests chan *collectortrace.ExportTraceServiceRequest
}

func (server *captureTraceServer) Export(_ context.Context, request *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	server.requests <- request
	return &collectortrace.ExportTraceServiceResponse{}, nil
}

func TestOTLPExporterEmitsOnlyBoundedHTTPAttributes(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	capture := &captureTraceServer{requests: make(chan *collectortrace.ExportTraceServiceRequest, 2)}
	collectortrace.RegisterTraceServiceServer(grpcServer, capture)
	go func() { _ = grpcServer.Serve(listener) }()
	defer grpcServer.Stop()
	runtime, err := New(t.Context(), Config{ServiceName: "identity-service", ServiceVersion: "test", Environment: "test", Region: "US", OTLPEndpoint: listener.Addr().String(), TraceSampleRatio: 1, AllowInsecureDevelopment: true})
	if err != nil {
		t.Fatal(err)
	}
	identifier := "private-invitation-identifier"
	request := httptest.NewRequest(http.MethodPost, "/v1/invitations/"+identifier+"/accept?token=private-token", nil)
	runtime.WrapHTTP(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })).ServeHTTP(httptest.NewRecorder(), request)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case exported := <-capture.requests:
		encoded := exported.String()
		if !strings.Contains(encoded, "/v1/invitations/{invitation_id}/accept") || strings.Contains(encoded, identifier) || strings.Contains(encoded, "private-token") || strings.Contains(encoded, "url.full") || strings.Contains(encoded, "url.path") {
			t.Fatalf("unsafe OTLP export: %s", encoded)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OTLP span was not exported")
	}
}
