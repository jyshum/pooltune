// Package admin serves only the private owner-facing control surface.
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"pooltune/internal/store"
	"sync"
	"time"
)

var ErrDenied = errors.New("administrator access denied")
var ErrConflict = errors.New("administrator revision conflict")

type Session struct {
	Token, CSRF string
	Expires     time.Time
}
type Auth struct {
	db       *store.Store
	lifetime time.Duration
	slots    chan struct{}
	mu       sync.Mutex
	window   time.Time
	attempts int
}

func NewAuth(db *store.Store, claim string, expires time.Time, lifetime time.Duration) (*Auth, error) {
	if db == nil || len(claim) != 43 || lifetime <= 0 || lifetime > 24*time.Hour {
		return nil, ErrDenied
	}
	hash := sha256.Sum256([]byte(claim))
	if e := db.InitAdmin(hash[:], expires); e != nil {
		return nil, e
	}
	return &Auth{db: db, lifetime: lifetime, slots: make(chan struct{}, 2)}, nil
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (a *Auth) enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.window) >= time.Minute {
		a.window = time.Now()
		a.attempts = 0
	}
	if a.attempts >= 10 {
		return false
	}
	a.attempts++
	select {
	case a.slots <- struct{}{}:
		return true
	default:
		return false
	}
}
func (a *Auth) newSession() (Session, []byte, error) {
	token, e := randomToken()
	if e != nil {
		return Session{}, nil, e
	}
	csrf, e := randomToken()
	if e != nil {
		return Session{}, nil, e
	}
	h := sha256.Sum256([]byte(token))
	return Session{Token: token, CSRF: csrf, Expires: time.Now().Add(a.lifetime)}, h[:], nil
}
func (a *Auth) Claim(token, password string) (Session, error) {
	if len(token) != 43 || len(password) < 12 || len(password) > 72 || !a.enter() {
		return Session{}, ErrDenied
	}
	defer func() { <-a.slots }()
	passwordHash, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return Session{}, ErrDenied
	}
	session, sessionHash, e := a.newSession()
	if e != nil {
		return Session{}, ErrDenied
	}
	claimHash := sha256.Sum256([]byte(token))
	if a.db.ClaimAdmin(claimHash[:], passwordHash, sessionHash, session.CSRF, session.Expires) != nil {
		return Session{}, ErrDenied
	}
	return session, nil
}
func (a *Auth) Login(password string) (Session, error) {
	if len(password) < 12 || len(password) > 72 || !a.enter() {
		return Session{}, ErrDenied
	}
	defer func() { <-a.slots }()
	passwordHash, e := a.db.AdminPasswordHash()
	if e != nil || len(passwordHash) == 0 || bcrypt.CompareHashAndPassword(passwordHash, []byte(password)) != nil {
		return Session{}, ErrDenied
	}
	session, hash, e := a.newSession()
	if e != nil || a.db.CreateAdminSession(passwordHash, hash, session.CSRF, session.Expires) != nil {
		return Session{}, ErrDenied
	}
	return session, nil
}
func (a *Auth) Session(token string) (Session, error) {
	if len(token) != 43 {
		return Session{}, ErrDenied
	}
	hash := sha256.Sum256([]byte(token))
	csrf, expires, e := a.db.AdminSession(hash[:])
	if e != nil {
		return Session{}, ErrDenied
	}
	return Session{Token: token, CSRF: csrf, Expires: expires}, nil
}
func (a *Auth) Logout(token string, all bool) error {
	if len(token) != 43 {
		return ErrDenied
	}
	hash := sha256.Sum256([]byte(token))
	if a.db.RevokeAdminSession(hash[:], all) != nil {
		return ErrDenied
	}
	return nil
}
func (a *Auth) Claimed() (bool, error) { hash, e := a.db.AdminPasswordHash(); return len(hash) > 0, e }
