package pgwire

import (
	"context"
	"encoding/binary"
	"github.com/jackc/pgx/v5/pgproto3"
)

// Cancellation retires this frontend lease. It cannot target a later operation
// on a reused session. PostgreSQL cancellation has no success acknowledgement;
// an admitted permit stays uncertain until independent backend reconciliation.
func (s *Server) cancelRequest(ctx context.Context, c *pgproto3.CancelRequest) {
	if len(c.SecretKey) != 4 {
		return
	}
	var key [8]byte
	binary.BigEndian.PutUint32(key[:4], c.ProcessID)
	copy(key[4:], c.SecretKey)
	s.mu.Lock()
	ss := s.cancels[key]
	if ss != nil {
		delete(s.cancels, key)
	}
	s.mu.Unlock()
	if ss == nil {
		return
	}
	ss.cancel()
	ss.client.Close()
	ss.upstream.Close()
	ctx, cancel := context.WithTimeout(ctx, s.cfg.HandshakeTimeout)
	defer cancel()
	up, err := s.dial(ctx)
	if err != nil {
		return
	}
	defer up.Close()
	data, _ := (&pgproto3.CancelRequest{ProcessID: ss.backendPID, SecretKey: ss.backendKey}).Encode(nil)
	_ = writeAll(up, data)
}
