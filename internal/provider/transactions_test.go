package provider

import "testing"

func TestTransactionCounterScopeAndValidation(t *testing.T) {
	good := []map[string]string{{"database": "other", "total_xact_count": "900"}, {"database": "app", "total_xact_count": "18446744073709551615"}}
	if n, e := transactionTotal(good, "app"); e != nil || n != ^uint64(0) {
		t.Fatalf("scoped uint64: %d %v", n, e)
	}
	for _, rows := range [][]map[string]string{
		nil, {{"database": "other", "total_xact_count": "5"}},
		{{"database": "app", "total_xact_count": "-1"}},
		{{"database": "app", "total_xact_count": "1.5"}},
		{{"database": "app", "total_xact_count": "18446744073709551616"}},
		{{"database": "app", "total_xact_count": ""}},
		{{"database": "app", "total_xact_count": "1"}, {"database": "app", "total_xact_count": "2"}},
	} {
		if _, e := transactionTotal(rows, "app"); e == nil {
			t.Fatalf("accepted bad counter: %v", rows)
		}
	}
	if n, e := transactionTotal([]map[string]string{{"database": "app", "total_xact_count": "0"}}, "app"); e != nil || n != 0 {
		t.Fatal("zero is a real reading")
	}
}
