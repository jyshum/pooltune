package control

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
)

// The anchor must be retained separately from traffic and control backups. It
// fences local epochs and detects rollback of those backups, not rollback of all
// three stores or ownership by a different machine.
type recoveryAnchor struct {
	Target    string
	Path      domain.PathKind
	Epoch     uint64
	Digest    string
	Authority string
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (e *Engine) openAnchor(data []byte) error {
	if e.cfg.RecoveryDirectory == "" {
		return nil
	}
	dir, err := filepath.EvalSymlinks(e.cfg.RecoveryDirectory)
	if err != nil {
		return ErrRecovery
	}
	traffic, err := filepath.EvalSymlinks(e.cfg.Directory)
	if err != nil {
		return ErrRecovery
	}
	rel, err := filepath.Rel(traffic, dir)
	if err != nil || rel == "." || (rel != ".." && !bytes.HasPrefix([]byte(rel), []byte("../"))) {
		return ErrRecovery
	}
	back, err := filepath.Rel(dir, traffic)
	if err != nil || back == "." || (back != ".." && !bytes.HasPrefix([]byte(back), []byte("../"))) {
		return ErrRecovery
	}
	e.anchor, err = durable.Open(dir)
	if err != nil {
		return ErrRecovery
	}
	raw, err := e.anchor.Load("anchor.json")
	if errors.Is(err, os.ErrNotExist) {
		if len(data) != 0 {
			return ErrRecovery
		}
		e.highWater = recoveryAnchor{Target: e.cfg.Target, Path: e.cfg.Path, Digest: digest(data), Epoch: e.cfg.Epoch, Authority: AuthorityChecksum(e.cfg.Guardrails, e.cfg.Registry)}
	} else {
		if err != nil || decode(raw, &e.highWater) != nil || e.highWater.Target != e.cfg.Target || e.highWater.Path != e.cfg.Path || e.highWater.Epoch >= e.cfg.Epoch || e.highWater.Digest != digest(data) || e.highWater.Authority != AuthorityChecksum(e.cfg.Guardrails, e.cfg.Registry) {
			return ErrRecovery
		}
		e.highWater.Epoch = e.cfg.Epoch
	}
	// Reserve the epoch before probing PostgreSQL. An interrupted attempt cannot
	// reuse it, even if no traffic snapshot was installed.
	return e.saveAnchor(e.highWater)
}
func (e *Engine) saveAnchor(a recoveryAnchor) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return ErrStorage
	}
	if err = e.anchor.Save("anchor.json", raw); err != nil {
		return ErrStorage
	}
	e.highWater = a
	return nil
}
