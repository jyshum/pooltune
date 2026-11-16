package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
)

func inspectRepairAccess(ctx context.Context, project supabaseproject.Project, scopeFile string, output io.Writer) error {
	scope, err := readRepairAccessScope(scopeFile)
	if err != nil {
		return err
	}
	if scope.Project != project.ID() {
		return errors.New("repair scope must match the connected project")
	}
	role, password := os.Getenv("POOLTUNE_REPAIR_DB_ROLE"), os.Getenv("POOLTUNE_REPAIR_DB_PASSWORD")
	if role == "" || password == "" {
		return errors.New("set POOLTUNE_REPAIR_DB_ROLE and POOLTUNE_REPAIR_DB_PASSWORD in the owner's environment")
	}
	ca, err := os.ReadFile(os.Getenv("POOLTUNE_PROJECT_CA_FILE"))
	if err != nil {
		return errors.New("set POOLTUNE_PROJECT_CA_FILE to the project's trusted Supabase CA certificate")
	}
	config, err := supabaseproject.DirectConfig(project.Ref(), role, password, ca)
	if err != nil {
		return err
	}
	config.RuntimeParams["application_name"] = "pooltune-project-repair-access"
	config.RuntimeParams["statement_timeout"] = "2s"
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return labdb.Diagnostic("connect to the Supabase project for repair access", err)
	}
	defer labdb.Close(db)

	report, err := hostedquery.ReadProjectRepairAccess(ctx, db, project, scope)
	if err != nil {
		return labdb.Diagnostic("check the project's repair access", err)
	}
	return json.NewEncoder(output).Encode(report)
}

func readRepairAccessScope(path string) (hostedquery.ProjectRepairAccessScope, error) {
	var scope hostedquery.ProjectRepairAccessScope
	if path == "" {
		return scope, errors.New("repair-access requires -scope with the project's repair selections and targets")
	}
	file, err := os.Open(path)
	if err != nil {
		return scope, errors.New("could not open the repair scope file")
	}
	defer file.Close()
	const maxScopeBytes = 16 << 10
	content, err := io.ReadAll(io.LimitReader(file, maxScopeBytes+1))
	if err != nil || len(content) > maxScopeBytes {
		return scope, errors.New("expected a valid repair scope of at most 16 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scope); err != nil {
		return scope, errors.New("expected a valid repair scope")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return scope, errors.New("expected a valid repair scope with one JSON object")
	}
	return scope, nil
}
