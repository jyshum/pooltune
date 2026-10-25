package httpingress

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"time"
)

// LoadVerifier accepts only public signing keys in an owner-pinned ES256 JWKS.
// Unknown/stale keys fall back at request time; no network discovery is performed.
func LoadVerifier(issuer, audience string, validUntil time.Time, raw []byte) (*Verifier, error) {
	invalid := errors.New("invalid public key snapshot")
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, invalid
	}
	var snapshot struct {
		Keys []struct{ Kty, Crv, Alg, Use, Kid, X, Y string }
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&snapshot) != nil || d.Decode(&struct{}{}) != io.EOF || len(snapshot.Keys) < 1 || len(snapshot.Keys) > 16 {
		return nil, invalid
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range snapshot.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Alg != "ES256" || k.Use != "sig" || keys[k.Kid] != nil {
			return nil, invalid
		}
		x, e := base64.RawURLEncoding.DecodeString(k.X)
		if e != nil || len(x) != 32 {
			return nil, invalid
		}
		y, e := base64.RawURLEncoding.DecodeString(k.Y)
		if e != nil || len(y) != 32 {
			return nil, invalid
		}
		keys[k.Kid] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	}
	return NewVerifier(issuer, audience, keys, validUntil)
}
