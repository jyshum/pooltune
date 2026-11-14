package httpingress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"strings"
	"time"
)

// Identity is scheduling evidence, never an upstream authorization replacement.
type Identity struct {
	Verified  bool
	Subject   string
	ExpiresAt time.Time
}

// Verifier holds an immutable, expiring ES256 public-key snapshot. Refresh/replacement
// is control-plane work. Unknown keys and stale snapshots receive no privileges.
type Verifier struct {
	issuer, audience string
	keys             map[string]*ecdsa.PublicKey
	validUntil       time.Time
}

func NewVerifier(issuer, audience string, keys map[string]*ecdsa.PublicKey, validUntil time.Time) (*Verifier, error) {
	if issuer == "" || audience == "" || len(keys) == 0 || len(keys) > 16 || !validUntil.After(time.Now()) {
		return nil, errors.New("invalid verification profile")
	}
	v := &Verifier{issuer: issuer, audience: audience, keys: make(map[string]*ecdsa.PublicKey), validUntil: validUntil}
	for id, key := range keys {
		if id == "" || len(id) > 128 || key == nil || key.Curve != elliptic.P256() || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
			return nil, errors.New("invalid ES256 key")
		}
		der, err := x509.MarshalPKIXPublicKey(key)
		if err != nil {
			return nil, err
		}
		copy, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, err
		}
		v.keys[id] = copy.(*ecdsa.PublicKey)
	}
	return v, nil
}
func (v *Verifier) Verify(authorization string, now time.Time) Identity {
	if v == nil || !now.Before(v.validUntil) || len(authorization) > 8192 || !strings.HasPrefix(authorization, "Bearer ") {
		return Identity{}
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(strings.TrimPrefix(authorization, "Bearer "), claims, func(t *jwt.Token) (any, error) {
		id, ok := t.Header["kid"].(string)
		if !ok || v.keys[id] == nil {
			return nil, errors.New("unknown key")
		}
		return v.keys[id], nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer(v.issuer), jwt.WithAudience(v.audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil || !token.Valid || claims.Subject == "" || len(claims.Subject) > 128 {
		return Identity{}
	}
	return Identity{Verified: true, Subject: claims.Subject, ExpiresAt: minTime(claims.ExpiresAt.Time, v.validUntil)}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
