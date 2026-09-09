// Package token mints and parses the self-contained daemon connection token.
//
// The connection token is an HS256 JWT whose HMAC key is the daemon credential
// itself. Because the key is the credential, the token is internally verifiable
// without any shared secret: anyone holding the credential (the application
// server, which receives it inside the token) can recompute the signature and
// confirm the claims were not tampered with.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Issuer identifies tokens produced by the daemon.
const Issuer = "software-factory-daemon"

// Errors returned by Parse. Callers can match these with errors.Is to
// distinguish malformed input, signature failures, and unexpected claims.
var (
	ErrMalformed        = errors.New("token is malformed")
	ErrInvalidSignature = errors.New("token signature is invalid")
	ErrUnexpectedAlg    = errors.New("token algorithm is not HS256")
)

// Claims carries the daemon connection details bundled inside the token.
type Claims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Endpoint string `json:"endpoint"`
	Name     string `json:"name"`
	Cred     string `json:"cred"`
	IssuedAt int64  `json:"iat"`
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

var encoding = base64.RawURLEncoding

// Sign produces a three-segment header.payload.signature JWT for the claims,
// signing with the HMAC-SHA256 of the supplied key.
func Sign(claims Claims, key string) (string, error) {
	if key == "" {
		return "", errors.New("signing key is required")
	}
	headerJSON, err := json.Marshal(header{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := encoding.EncodeToString(headerJSON) + "." + encoding.EncodeToString(payloadJSON)
	signature := sign(signingInput, key)
	return signingInput + "." + signature, nil
}

// Parse decodes the claims and verifies the HS256 signature against key.
func Parse(tokenString, key string) (Claims, error) {
	segments := strings.Split(tokenString, ".")
	if len(segments) != 3 {
		return Claims{}, fmt.Errorf("%w: expected three segments", ErrMalformed)
	}
	headerJSON, err := encoding.DecodeString(segments[0])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: header is not base64url", ErrMalformed)
	}
	var head header
	if err = json.Unmarshal(headerJSON, &head); err != nil {
		return Claims{}, fmt.Errorf("%w: header is not JSON", ErrMalformed)
	}
	if head.Alg != "HS256" {
		return Claims{}, ErrUnexpectedAlg
	}
	signingInput := segments[0] + "." + segments[1]
	expected := sign(signingInput, key)
	if !hmac.Equal([]byte(expected), []byte(segments[2])) {
		return Claims{}, ErrInvalidSignature
	}
	payloadJSON, err := encoding.DecodeString(segments[1])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: payload is not base64url", ErrMalformed)
	}
	var claims Claims
	if err = json.Unmarshal(payloadJSON, &claims); err != nil {
		return Claims{}, fmt.Errorf("%w: payload is not JSON", ErrMalformed)
	}
	return claims, nil
}

func sign(signingInput, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(signingInput))
	return encoding.EncodeToString(mac.Sum(nil))
}
