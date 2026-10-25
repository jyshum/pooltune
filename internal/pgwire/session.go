package pgwire

import (
	"errors"
	"github.com/jackc/pgx/v5/pgproto3"
)

var ErrUnsupported = errors.New("unsupported PostgreSQL protocol profile")

// flow binds one permit across complete batches and explicit transactions.
// Backend messages are forwarded as they arrive, including before Flush/Sync.
type flow struct{ held, extended, waiting bool }

func (f *flow) before(m pgproto3.FrontendMessage) (bool, error) {
	if _, ok := m.(*pgproto3.Terminate); ok {
		return false, nil
	}
	if f.waiting {
		return false, ErrUnsupported
	}
	switch v := m.(type) {
	case *pgproto3.Query:
		if f.extended {
			return false, ErrUnsupported
		}
		f.waiting = true
	case *pgproto3.Parse:
		if v.Name != "" {
			return false, ErrUnsupported
		}
		f.extended = true
	case *pgproto3.Bind:
		if v.DestinationPortal != "" || v.PreparedStatement != "" {
			return false, ErrUnsupported
		}
		f.extended = true
	case *pgproto3.Describe:
		if v.Name != "" || (v.ObjectType != 'S' && v.ObjectType != 'P') {
			return false, ErrUnsupported
		}
		f.extended = true
	case *pgproto3.Close:
		if v.Name != "" || (v.ObjectType != 'S' && v.ObjectType != 'P') {
			return false, ErrUnsupported
		}
		f.extended = true
	case *pgproto3.Execute:
		if v.Portal != "" || v.MaxRows != 0 {
			return false, ErrUnsupported
		}
		f.extended = true
	case *pgproto3.Sync:
		f.waiting = true
		f.extended = false
	case *pgproto3.Flush:
		return false, nil
	default:
		return false, ErrUnsupported
	}
	acquire := !f.held
	f.held = true
	return acquire, nil
}
func (f *flow) ready(status byte) (bool, error) {
	if !f.waiting || !f.held {
		return false, ErrUnsupported
	}
	if status != 'I' && status != 'T' && status != 'E' {
		return false, ErrUnsupported
	}
	f.waiting = false
	f.extended = false
	if status == 'I' {
		f.held = false
		return true, nil
	}
	return false, nil
}
