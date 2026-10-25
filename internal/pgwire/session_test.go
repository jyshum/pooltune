package pgwire

import (
	"github.com/jackc/pgx/v5/pgproto3"
	"testing"
)

func TestTransactionPermitSurvivesFailedTransaction(t *testing.T) {
	var p flow
	acquire, err := p.before(&pgproto3.Query{String: "BEGIN; SELECT 1"})
	if err != nil || !acquire {
		t.Fatal("first batch not admitted")
	}
	release, err := p.ready('T')
	if err != nil || release {
		t.Fatal("released inside transaction")
	}
	acquire, err = p.before(&pgproto3.Query{String: "SELECT 1/0"})
	if err != nil || acquire {
		t.Fatal("reacquired transaction permit")
	}
	release, err = p.ready('E')
	if err != nil || release {
		t.Fatal("released failed transaction")
	}
	acquire, err = p.before(&pgproto3.Query{String: "ROLLBACK"})
	if err != nil || acquire {
		t.Fatal("rollback waited for another permit")
	}
	release, err = p.ready('I')
	if err != nil || !release {
		t.Fatal("idle did not release")
	}
}

func TestExtendedFlushBeforeSync(t *testing.T) {
	var p flow
	for i, m := range []pgproto3.FrontendMessage{&pgproto3.Parse{Query: "select $1::int"}, &pgproto3.Describe{ObjectType: 'S'}, &pgproto3.Flush{}, &pgproto3.Bind{}, &pgproto3.Execute{}, &pgproto3.Sync{}} {
		a, e := p.before(m)
		if e != nil || a != (i == 0) {
			t.Fatalf("message %T: acquire=%v error=%v", m, a, e)
		}
	}
	if r, e := p.ready('I'); e != nil || !r {
		t.Fatal("extended cycle did not release")
	}
}

func TestRejectUnsupportedWithoutNewAdmission(t *testing.T) {
	for _, m := range []pgproto3.FrontendMessage{&pgproto3.Parse{Name: "named"}, &pgproto3.Bind{DestinationPortal: "named"}, &pgproto3.Execute{MaxRows: 1}, &pgproto3.CopyData{}, &pgproto3.FunctionCall{}} {
		var p flow
		if a, e := p.before(m); e == nil || a || p.held {
			t.Fatalf("accepted unsupported %T", m)
		}
	}
	var p flow
	p.before(&pgproto3.Query{String: "select 1"})
	if _, e := p.before(&pgproto3.Query{String: "select 2"}); e == nil {
		t.Fatal("pipeline allowed")
	}
	if _, e := p.ready('X'); e == nil {
		t.Fatal("invalid backend status accepted")
	}
}
