package hostedquery

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
)

func TestParseOwnedCronFixturePinsTrustedWriter(t *testing.T) {
	settings, ca := cronObserverTestFixture(t)
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := ParseOwnedCronFixture(string(encoded), "fixture-password", ca)
	if err != nil || fixture.Config.User != settings.Request.Candidate.Schema || fixture.Config.TLSConfig.ServerName != fixture.Config.Host ||
		fixture.Request.Target != settings.Request.Target || fixture.QueryID != 0 {
		t.Fatalf("exact Rookery fixture failed to parse: %v", err)
	}
	for _, invalid := range []string{
		string(encoded) + ` {}`,
		strings.Repeat(" ", 20000) + string(encoded),
		strings.Replace(string(encoded), "Installation", "Unrecognized", 1),
		strings.Replace(string(encoded), settings.Installation, "9876543210abcdef", 1),
		strings.Replace(string(encoded), settings.Request.Target.CommandDigest, strings.Repeat("0", 64), 1),
	} {
		if _, err := ParseOwnedCronFixture(invalid, "fixture-password", ca); err == nil {
			t.Fatal("accepted malformed or mismatched fixture binding")
		}
	}
}

func TestDecodeOwnedCronPeriodPreservesObservedQueryID(t *testing.T) {
	const queryID = -9223372036854775743
	period := CronPeriod{Busy: CronWindow{Target: CronMeasure{Before: Counter{Query: queryID}}}, Blocks: []CronBlock{{Query: queryID}}}
	encoded, err := json.Marshal(period)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOwnedCronPeriod(bytes.NewReader(encoded))
	if err != nil || decoded.Busy.Target.Before.Query != queryID || decoded.Blocks[0].Query != queryID {
		t.Fatalf("lost JavaScript-safe query identity: %+v %v", decoded, err)
	}
	for _, invalid := range []string{
		"null", string(encoded) + ` {}`, string(encoded) + ` noise`,
		strings.Replace(string(encoded), `"cycle"`, `"unknown"`, 1),
		strings.Repeat(" ", 1<<20) + string(encoded),
		string(encoded) + strings.Repeat(" ", 1<<20),
	} {
		if _, err := DecodeOwnedCronPeriod(strings.NewReader(invalid)); err == nil {
			t.Fatal("accepted incomplete, oversized, or compound period input")
		}
	}
}

func TestObservedCronPeriodBindsAutomaticCounterWithoutChangingFixture(t *testing.T) {
	settings, ca := cronObserverTestFixture(t)
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := ParseOwnedCronFixture(string(encoded), "fixture-password", ca)
	if err != nil {
		t.Fatal(err)
	}
	now := settings.Request.FirstCycle
	observed := Counter{Database: settings.Request.Target.DatabaseOID, Role: settings.Request.Target.OwnerOID, Query: -9223372036854775743,
		Boot: now.Add(-time.Hour), Reset: now.Add(-time.Minute), Entry: now.Add(-time.Second)}
	period := CronPeriod{Busy: CronWindow{Target: CronMeasure{Before: observed}}}
	req, err := fixture.requestForObservedPeriod(period)
	if err != nil || req.Observed != observed || req.Target != fixture.Request.Target || fixture.Request.Observed != (Counter{}) {
		t.Fatalf("automatic recorder changed fixture instead of binding observation: %+v %v", req, err)
	}
	fixture.QueryID = observed.Query + 1
	if _, err := fixture.requestForObservedPeriod(period); err == nil {
		t.Fatal("recorder replaced a configured query")
	}
	fixture.QueryID = 0
	period.Busy.Target.Before.Role++
	if _, err := fixture.requestForObservedPeriod(period); err == nil {
		t.Fatal("recorder accepted a foreign role")
	}
}

func cronObserverTestFixture(t *testing.T) (ownedCronFixtureSettings, []byte) {
	t.Helper()
	const key = "0123456789abcdef"
	project, err := supabaseproject.New(OwnedIndexRookeryRef, key)
	if err != nil {
		t.Fatal(err)
	}
	role := project.OwnedRepairProbeRole("cron")
	first := cronCycle(time.Now())
	digest := strings.Repeat("a", 64)
	settings := ownedCronFixtureSettings{Installation: key, Request: OwnedCronRequest{
		Target: ProjectCronTarget{Project: project.ID(), Name: role + "_target", DatabaseOID: 11, OwnerOID: 22, JobID: 33, WitnessJobID: 44,
			CommandDigest: ownedCronDigest("CALL " + qualified(role, cronProcedure) + "(200)")},
		Candidate: ProjectIndexTarget{Schema: role, Relation: "candidate", DatabaseOID: 11, TableOID: 99},
		ReadOID:   50, ProcedureOID: 51, ReadDigest: digest, ProcedureDigest: digest, FirstCycle: first, Expires: first.Add(20 * time.Minute),
	}}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return settings, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
