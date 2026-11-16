package hostedapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"pooltune/internal/hostedapp/auth"
	"pooltune/internal/hostedquery"

	"github.com/jackc/pgx/v5"
)

type repairWorkerPolicy struct {
	Project      string   `json:"project"`
	Epoch        int64    `json:"epoch"`
	Capabilities []string `json:"capabilities"`
	Paused       bool     `json:"paused"`
}

type repairWorkerOperation struct {
	Operation string `json:"operation"`
	Status    string `json:"status"`
}

type repairWorkerReport struct {
	Epoch      int64                   `json:"epoch"`
	Operations []repairWorkerOperation `json:"operations"`
}

func (a *App) changeRepairWorkerKey(w http.ResponseWriter, r *http.Request, user auth.User) {
	row, ok := a.ownedProject(w, r, user)
	if !ok {
		return
	}
	if !row.Configured || r.ParseForm() != nil {
		http.Error(w, "Connect the project first.", http.StatusBadRequest)
		return
	}
	switch r.PostForm.Get("mode") {
	case "revoke":
		if _, err := a.projects.db.Exec(r.Context(), `DELETE FROM pooltune_app.repair_worker_links l
			USING pooltune_app.projects p WHERE l.project_id=p.id AND p.id=$1 AND p.owner_id=$2`, row.ID, row.Owner); err != nil {
			http.Error(w, "Could not revoke the worker key.", http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, "/app/projects/"+row.ID, http.StatusSeeOther)
	case "issue":
		selected, err := a.vault.open(row, row.Secret)
		if err != nil || len(selected.Capabilities) == 0 {
			http.Error(w, "Select a repair before linking a worker.", http.StatusBadRequest)
			return
		}
		var keyBytes [32]byte
		if _, err := rand.Read(keyBytes[:]); err != nil {
			http.Error(w, "Could not create the worker key.", http.StatusServiceUnavailable)
			return
		}
		key := hex.EncodeToString(keyBytes[:])
		digest := sha256.Sum256(keyBytes[:])
		if err := a.projects.replaceRepairWorkerKey(r.Context(), row, digest); err != nil {
			http.Error(w, "Could not create the worker key. Refresh the project and try again.", http.StatusConflict)
			return
		}
		a.render(w, http.StatusOK, "repair-worker-key", Page{
			Title: "Repair worker key", User: user.Email, Project: baseProjectView(row), RepairWorkerKey: key,
		})
	default:
		http.Error(w, "Choose create or revoke.", http.StatusBadRequest)
	}
}

func (s projectStore) replaceRepairWorkerKey(ctx context.Context, row projectRecord, digest [32]byte) error {
	tag, err := s.db.Exec(ctx, `WITH active AS MATERIALIZED (
		SELECT id FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND epoch>0 FOR UPDATE
	) INSERT INTO pooltune_app.repair_worker_links(project_id,token_digest) SELECT id,$4 FROM active
	ON CONFLICT(project_id) DO UPDATE SET token_digest=excluded.token_digest,report_epoch=NULL,report=NULL,reported_at=NULL`,
		row.ID, row.Owner, row.Epoch, digest[:])
	if err == nil && tag.RowsAffected() != 1 {
		return hostedquery.ErrProjectDisconnected
	}
	return err
}

func repairWorkerDigest(header string) ([32]byte, bool) {
	token, found := strings.CutPrefix(header, "Bearer ")
	var key [32]byte
	if !found || len(token) != 2*len(key) || strings.ToLower(token) != token {
		return key, false
	}
	if _, err := hex.Decode(key[:], []byte(token)); err != nil {
		return key, false
	}
	return sha256.Sum256(key[:]), true
}

func (a *App) repairWorker(w http.ResponseWriter, r *http.Request) {
	digest, valid := repairWorkerDigest(r.Header.Get("Authorization"))
	if !valid {
		http.Error(w, "Worker key required.", http.StatusUnauthorized)
		return
	}
	var row projectRecord
	err := a.projects.db.QueryRow(r.Context(), `SELECT p.id,p.ref,p.owner_id,p.epoch,p.secret
		FROM pooltune_app.projects p JOIN pooltune_app.repair_worker_links l ON l.project_id=p.id
		WHERE p.id=$1 AND p.epoch>0 AND l.token_digest=$2`, r.PathValue("id"), digest[:]).
		Scan(&row.ID, &row.Ref, &row.Owner, &row.Epoch, &row.Secret)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Worker key unavailable.", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "Could not load the worker policy.", http.StatusServiceUnavailable)
		return
	}
	identity, identityErr := row.identity()
	secrets, secretErr := a.vault.open(row, row.Secret)
	if identityErr != nil || secretErr != nil {
		http.Error(w, "Could not load the worker policy.", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet {
		policy := repairWorkerPolicy{Project: identity.ID(), Epoch: row.Epoch,
			Capabilities: append([]string{}, secrets.Capabilities...), Paused: secrets.RepairsPaused}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(policy)
		return
	}
	a.receiveRepairWorkerReport(w, r, row, digest, secrets)
}

func (a *App) receiveRepairWorkerReport(w http.ResponseWriter, r *http.Request, row projectRecord, digest [32]byte, secrets projectSecrets) {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var report repairWorkerReport
	if err := dec.Decode(&report); err != nil || !errors.Is(dec.Decode(new(any)), io.EOF) ||
		!validRepairWorkerReport(report, secrets.Capabilities) {
		http.Error(w, "Invalid repair access report.", http.StatusBadRequest)
		return
	}
	if secrets.RepairsPaused || report.Epoch != row.Epoch {
		http.Error(w, "The repair policy changed. Refresh it before reporting.", http.StatusConflict)
		return
	}
	encoded, err := json.Marshal(report.Operations)
	if err != nil {
		http.Error(w, "Could not encode the repair access report.", http.StatusBadRequest)
		return
	}
	tag, err := a.projects.db.Exec(r.Context(), `WITH active AS MATERIALIZED (
		SELECT id FROM pooltune_app.projects WHERE id=$1 AND owner_id=$2 AND epoch=$3 AND epoch>0 FOR UPDATE
	) UPDATE pooltune_app.repair_worker_links l SET report_epoch=$3,report=$5::jsonb,reported_at=statement_timestamp()
	FROM active WHERE l.project_id=active.id AND l.token_digest=$4`, row.ID, row.Owner, row.Epoch, digest[:], string(encoded))
	if err != nil {
		http.Error(w, "Could not save the repair access report.", http.StatusServiceUnavailable)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "The repair policy changed. Refresh it before reporting.", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validRepairWorkerReport(report repairWorkerReport, selected []string) bool {
	if report.Epoch <= 0 || len(report.Operations) > 8 {
		return false
	}
	seen := make(map[string]bool, len(report.Operations))
	for _, result := range report.Operations {
		capability, known := hostedquery.RepairAccessCapability(result.Operation)
		if !known || !slices.Contains(selected, capability) || seen[result.Operation] {
			return false
		}
		switch result.Status {
		case "granted", "database_permission_missing", "target_required", "target_changed":
		case "cron_window_mismatch":
			if result.Operation != "cron" {
				return false
			}
		default:
			return false
		}
		seen[result.Operation] = true
	}
	return true
}

func (a *App) repairWorkerStatus(ctx context.Context, row projectRecord) (bool, string) {
	var raw []byte
	err := a.projects.db.QueryRow(ctx, `SELECT CASE WHEN report_epoch=$2
		AND reported_at>statement_timestamp()-interval '90 seconds' THEN report END
		FROM pooltune_app.repair_worker_links WHERE project_id=$1`, row.ID, row.Epoch).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "Create a key to link the Go worker running in your environment."
	}
	if err != nil {
		return false, "Worker status is temporarily unavailable."
	}
	if len(raw) == 0 {
		return true, "Waiting for a current permission check from the worker."
	}
	var results []repairWorkerOperation
	if json.Unmarshal(raw, &results) != nil {
		return true, "Worker status is temporarily unavailable."
	}
	granted := 0
	for _, result := range results {
		if result.Status == "granted" {
			granted++
		}
	}
	return true, fmt.Sprintf("Database access reported for %d of %d actions checked. Automatic execution still needs the repair queue.", granted, len(results))
}
