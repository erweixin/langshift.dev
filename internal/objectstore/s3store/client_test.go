package s3store

import (
	"context"
	"errors"
	"testing"
)

func TestClientConfigurationRejectsCredentialURLsAndRemotePlaintext(t *testing.T) {
	for _, configuration := range []ClientConfig{
		{Region: "us-east-1", Endpoint: "http://s3.internal:9000", AllowInsecureDevelopment: true},
		{Region: "us-east-1", Endpoint: "https://key:secret@s3.internal"},
		{Region: "", Endpoint: "https://s3.internal"},
	} {
		if _, err := NewClient(context.Background(), configuration); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("config=%#v err=%v", configuration, err)
		}
	}
}
