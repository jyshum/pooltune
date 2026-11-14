package httpingress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPublicSnapshotRejectsPrivateMalformedDuplicateKeys(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	b64 := base64.RawURLEncoding.EncodeToString
	keyJSON := fmt.Sprintf(`{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":"k1","x":"%s","y":"%s"}`, b64(key.X.FillBytes(make([]byte, 32))), b64(key.Y.FillBytes(make([]byte, 32))))
	valid := `{"keys":[` + keyJSON + `]}`
	if _, e := LoadVerifier("issuer", "aud", time.Now().Add(time.Hour), []byte(valid)); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(valid, `"use":"sig"`, `"use":"enc"`, 1), strings.Replace(valid, `"kty":"EC"`, `"d":"secret","kty":"EC"`, 1), strings.Replace(valid, `"crv":"P-256"`, `"crv":"P-384"`, 1), `{"keys":[` + keyJSON + `,` + keyJSON + `]}`, valid + `{}`} {
		if _, e := LoadVerifier("issuer", "aud", time.Now().Add(time.Hour), []byte(bad)); e == nil {
			t.Fatal("unsafe key snapshot accepted")
		}
	}
}
