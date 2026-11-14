// Command pooltune-query-lab runs the bounded hosted-query M1 fixture.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pooltune/internal/hostedquery"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	rookeryHost = "aws-0-us-west-2.pooler.supabase.com"
	rookeryUser = "postgres.osdjqwpzgyzwfaxyufze"
)

func main() {
	route := flag.String("target", "local", "fixture route: local or rookery")
	trial := flag.String("index-trial", "", "automatic index fixture: anthropic or scripted")
	recoverOnly := flag.Bool("recover-index-trials", false, "reconcile interrupted fixture runs")
	flag.Parse()
	if (*trial != "" && *trial != "anthropic" && *trial != "scripted") || (*recoverOnly && *trial != "") {
		fmt.Fprintln(os.Stderr, "choose an explicit index trial mode or recovery")
		os.Exit(2)
	}
	if flag.NArg() != 0 || (*route != "local" && *route != "rookery") {
		fmt.Fprintln(os.Stderr, "select -target local or -target rookery and supply connection settings in the environment")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	err := run(ctx, *route == "rookery", *trial, *recoverOnly)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, rookery bool, trial string, recoverOnly bool) error {
	stateConfig, err := labConfig(os.Getenv("POOLTUNE_LAB_STATE_DSN"), false)
	if err != nil {
		return fmt.Errorf("state configuration: %w", err)
	}
	targetConfig, err := labConfig(os.Getenv("POOLTUNE_LAB_TARGET_DSN"), rookery)
	if err != nil {
		return fmt.Errorf("target configuration: %w", err)
	}
	if stateConfig.Host == targetConfig.Host && stateConfig.Port == targetConfig.Port &&
		stateConfig.Database == targetConfig.Database {
		return errors.New("state and target must use separate databases")
	}
	var investigator hostedquery.IndexInvestigator
	if trial != "" {
		investigator, err = processInvestigator(trial)
		if err != nil {
			return err
		}
	}
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		return diagnostic("connect Pooltune state", err)
	}
	defer closeConnection(state)
	if err := hostedquery.PrepareState(ctx, state); err != nil {
		return diagnostic("prepare Pooltune state", err)
	}
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		return diagnostic("connect fixture target", err)
	}
	defer closeConnection(target)

	identity := "local"
	if rookery {
		identity = "rookery"
	}
	if recoverOnly {
		if err := hostedquery.RecoverIndexTrials(ctx, target, state, identity); err != nil {
			return diagnostic("recover fixture", err)
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "reconciled"})
	}
	if trial != "" {
		report, err := hostedquery.RunIndexTrial(ctx, target, state, investigator, identity)
		if err != nil {
			if report.RunID != "" {
				fmt.Fprintf(os.Stderr, "Index trial %s has a journal record. Use -recover-index-trials on the same target if pending.\n", report.RunID)
			}
			return diagnostic("run index trial", err)
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Investigator string                       `json:"investigator"`
			Report       hostedquery.IndexTrialReport `json:"report"`
		}{Investigator: trial, Report: report})
	}
	report, err := hostedquery.RunLab(ctx, target)
	if err != nil {
		return diagnostic("run fixture", err)
	}
	id, err := hostedquery.RecordReport(ctx, state, report)
	if err != nil {
		return diagnostic("store report", err)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		ID     int64              `json:"id"`
		Report hostedquery.Report `json:"report"`
	}{ID: id, Report: report})
}

func processInvestigator(mode string) (hostedquery.IndexInvestigator, error) {
	if mode == "anthropic" && (os.Getenv("ANTHROPIC_API_KEY") == "" || os.Getenv("POOLTUNE_ANTHROPIC_MODEL") == "") {
		return nil, errors.New("anthropic mode requires ANTHROPIC_API_KEY and POOLTUNE_ANTHROPIC_MODEL")
	}
	path, err := filepath.Abs("investigator/run.ts")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, errors.New("run from the Pooltune repo with investigator dependencies installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("Node.js is required for the investigator")
	}
	return func(ctx context.Context, e hostedquery.IndexEvidence) (hostedquery.IndexProposal, error) {
		ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
		defer cancel()
		payload, err := json.Marshal(e)
		if err != nil {
			return hostedquery.IndexProposal{}, err
		}
		call := exec.CommandContext(ctx, node, path, mode)
		call.Stdin = bytes.NewReader(payload)
		call.Env = []string{"POOLTUNE_LAB_STATE_DSN=" + os.Getenv("POOLTUNE_LAB_STATE_DSN"), "LANGSMITH_TRACING=false"}
		if mode == "anthropic" {
			call.Env = append(call.Env, "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
				"POOLTUNE_ANTHROPIC_MODEL="+os.Getenv("POOLTUNE_ANTHROPIC_MODEL"))
		}
		response, err := call.Output()
		if err != nil {
			return hostedquery.IndexProposal{}, errors.New("investigator process failed")
		}
		if len(response) > 4096 {
			return hostedquery.IndexProposal{}, errors.New("oversized investigator response")
		}
		var proposal hostedquery.IndexProposal
		decoder := json.NewDecoder(bytes.NewReader(response))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&proposal); err != nil || !json.Valid(response) {
			return hostedquery.IndexProposal{}, errors.New("invalid investigator response")
		}
		return proposal, nil
	}, nil
}

func labConfig(dsn string, rookery bool) (*pgx.ConnConfig, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.User == nil || u.Fragment != "" {
		return nil, errors.New("expected a PostgreSQL URL")
	}
	password, hasPassword := u.User.Password()
	db := strings.TrimPrefix(u.Path, "/")
	port, portErr := strconv.ParseUint(u.Port(), 10, 16)
	if !hasPassword || password == "" || u.User.Username() == "" || db == "" ||
		strings.Contains(db, "/") || portErr != nil || port == 0 {
		return nil, errors.New("URL requires a user, password, port and database")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["sslmode"]) != 1 {
		return nil, errors.New("URL requires exactly one sslmode parameter")
	}

	mode := query.Get("sslmode")
	var tlsConfig *tls.Config
	if rookery {
		if u.Hostname() != rookeryHost || port != 5432 || u.User.Username() != rookeryUser ||
			db != "postgres" || mode != "verify-full" {
			return nil, errors.New("expected the Rookery session pooler with verify-full")
		}
		ca, err := os.ReadFile(os.Getenv("POOLTUNE_LAB_ROOKERY_CA_FILE"))
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(ca) {
			return nil, errors.New("supply the Supabase CA with POOLTUNE_LAB_ROOKERY_CA_FILE")
		}
		tlsConfig = &tls.Config{RootCAs: roots, ServerName: rookeryHost, MinVersion: tls.VersionTLS12}
	} else if u.Hostname() != "127.0.0.1" || mode != "disable" {
		return nil, errors.New("expected a local disposable database at 127.0.0.1 with sslmode=disable")
	}
	if os.Getenv("PGSERVICE") != "" {
		return nil, errors.New("unset PGSERVICE when running the lab")
	}
	// Start from a fixed pgx configuration, then assign only the validated connection fields.
	config, err := pgx.ParseConfig("postgres://lab:placeholder@127.0.0.1:5432/lab?sslmode=disable&target_session_attrs=any&connect_timeout=5")
	if err != nil {
		return nil, errors.New("cannot initialize PostgreSQL driver")
	}
	config.Host, config.Port, config.Database = u.Hostname(), uint16(port), db
	config.User, config.Password = u.User.Username(), password
	config.RuntimeParams = map[string]string{"application_name": "pooltune-query-lab"}
	config.Fallbacks = nil
	config.TLSConfig = tlsConfig
	config.DialFunc = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	return config, nil
}

func closeConnection(connection *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection.Close(ctx)
}

// Keep raw driver errors out of CLI output. They may contain credentials or database details.
func diagnostic(stage string, err error) error {
	var pgError *pgconn.PgError
	switch {
	case errors.As(err, &pgError):
		return fmt.Errorf("%s failed (SQLSTATE %s)", stage, pgError.Code)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s timed out", stage)
	default:
		return fmt.Errorf("%s failed", stage)
	}
}
