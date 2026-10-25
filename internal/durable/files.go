// Package durable owns private, bounded, fsynced local snapshots on Unix hosts.
package durable

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var ErrInvalid = errors.New("invalid durable state")
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,180}$`)

const MaxBytes = 4 << 20

type Files struct {
	mu     sync.Mutex
	root   *os.Root
	lock   *os.File
	closed bool
}

func Open(dir string) (*Files, error) {
	if !filepath.IsAbs(dir) {
		return nil, ErrInvalid
	}
	info, e := os.Lstat(dir)
	if e != nil {
		return nil, e
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	root, e := os.OpenRoot(dir)
	if e != nil {
		return nil, e
	}
	lock, e := root.OpenFile("owner.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if e != nil {
		root.Close()
		return nil, e
	}
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		lock.Close()
		root.Close()
		return nil, e
	}
	return &Files{root: root, lock: lock}, nil
}
func (s *Files) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	_ = s.lock.Close()
	return s.root.Close()
}
func (s *Files) Load(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !namePattern.MatchString(name) {
		return nil, ErrInvalid
	}
	f, e := s.root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > MaxBytes {
		return nil, ErrInvalid
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if len(b) > MaxBytes {
		return nil, ErrInvalid
	}
	return b, e
}
func (s *Files) Save(name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !namePattern.MatchString(name) || len(data) > MaxBytes {
		return ErrInvalid
	}
	random := make([]byte, 16)
	if _, e := rand.Read(random); e != nil {
		return e
	}
	tmp := "tmp-" + hex.EncodeToString(random)
	f, e := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer s.root.Remove(tmp)
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = s.root.Rename(tmp, name); e != nil {
		return e
	}
	dir, e := s.root.Open(".")
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

// InheritLock duplicates the open ownership description for a managed child.
// The caller closes this duplicate after exec. On abrupt parent death the child
// retains the flock until exit; normal Close must follow child termination.
func (s *Files) InheritLock() (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrInvalid
	}
	fd, err := unix.Dup(int(s.lock.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "pooltune-owner-lease"), nil
}
