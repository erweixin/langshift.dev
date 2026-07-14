package email

import "testing"

func TestNormalizeCanonicalizesWithoutAcceptingDisplayNames(t *testing.T) {
	got, err := Normalize("  User.Name@Example.COM ")
	if err != nil || got != "user.name@example.com" {
		t.Fatalf("normalized=%q err=%v", got, err)
	}
	for _, invalid := range []string{"", "Name <user@example.com>", "missing-at.example.com", "a@example.com\nBcc:x@y.test"} {
		if _, err = Normalize(invalid); err == nil {
			t.Fatalf("invalid email accepted: %q", invalid)
		}
	}
}
