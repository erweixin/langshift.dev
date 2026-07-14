package cursor

import (
	"bytes"
	"errors"
	"testing"
)

func TestCodecBindsCursorToScopeAndRejectsTampering(t *testing.T) {
	codec := Codec{Key: bytes.Repeat([]byte{0x42}, 32)}
	token, err := codec.Encode("sessions:user-1", struct {
		ID string `json:"id"`
	}{ID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err = codec.Decode(token, "sessions:user-1", &decoded); err != nil || decoded.ID != "session-1" {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	for _, invalid := range []struct{ token, scope string }{{token + "x", "sessions:user-1"}, {token, "sessions:user-2"}} {
		if err = codec.Decode(invalid.token, invalid.scope, &decoded); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid cursor error=%v", err)
		}
	}
}
