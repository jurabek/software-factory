package token

import (
	"errors"
	"strings"
	"testing"
)

func sampleClaims() Claims {
	return Claims{
		Issuer:   Issuer,
		Subject:  "0123456789abcdef0123456789abcdef",
		Endpoint: "http://127.0.0.1:8080",
		Name:     "sandbox-a",
		Cred:     "daemon-test-credential-with-32-characters",
		IssuedAt: 1_700_000_000,
	}
}

func TestSignProducesThreeBase64URLSegments(t *testing.T) {
	signed, err := Sign(sampleClaims(), "key-material-with-enough-length")
	if err != nil {
		t.Fatal(err)
	}
	segments := strings.Split(signed, ".")
	if len(segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(segments))
	}
	payload, err := encoding.DecodeString(segments[1])
	if err != nil {
		t.Fatalf("payload not base64url: %v", err)
	}
	if !strings.Contains(string(payload), "\"endpoint\":\"http://127.0.0.1:8080\"") {
		t.Fatalf("payload = %s", payload)
	}
}

func TestRoundTripReturnsIdenticalClaims(t *testing.T) {
	key := "daemon-test-credential-with-32-characters"
	claims := sampleClaims()
	signed, err := Sign(claims, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(signed, key)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != claims {
		t.Fatalf("parsed = %#v, want %#v", parsed, claims)
	}
}

func TestParseRejectsTamperedPayload(t *testing.T) {
	key := "daemon-test-credential-with-32-characters"
	signed, err := Sign(sampleClaims(), key)
	if err != nil {
		t.Fatal(err)
	}
	segments := strings.Split(signed, ".")
	tampered := Claims{Cred: key, Endpoint: "http://evil.example"}
	forged, err := Sign(tampered, "another-key")
	if err != nil {
		t.Fatal(err)
	}
	forgedPayload := strings.Split(forged, ".")[1]
	mutated := segments[0] + "." + forgedPayload + "." + segments[2]
	if _, err = Parse(mutated, key); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v, want ErrInvalidSignature", err)
	}
}

func TestParseRejectsWrongKey(t *testing.T) {
	signed, err := Sign(sampleClaims(), "correct-key-material")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(signed, "wrong-key-material"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("error = %v, want ErrInvalidSignature", err)
	}
}

func TestParseRejectsMalformedTokens(t *testing.T) {
	for _, name := range []string{"", "onlyone", "two.parts", "a.b.c.d"} {
		if _, err := Parse(name, "key"); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Parse(%q) error = %v, want ErrMalformed", name, err)
		}
	}
}

func TestSignRequiresKey(t *testing.T) {
	if _, err := Sign(sampleClaims(), ""); err == nil {
		t.Fatal("Sign succeeded with empty key")
	}
}
