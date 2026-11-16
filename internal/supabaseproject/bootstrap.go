package supabaseproject

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var (
	ErrSetupPermission  = errors.New("the setup token needs Database Write access to this Supabase project")
	ErrSetupUnavailable = errors.New("Supabase could not complete automatic setup. Check the project and token, then retry")
)

var setupHTTP = &http.Client{
	Timeout: 9 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

const OwnedIndexProbeRef = "osdjqwpzgyzwfaxyufze"

// Bootstrap uses the one-time setup token only to install the project's
// dedicated monitor. The hosted worker never receives this token.
func (p Project) Bootstrap(ctx context.Context, token, password string) error {
	return p.bootstrap(ctx, token, password, "", false)
}

// BootstrapOwnedIndexProbe changes the discovery objective for only the
// separate synthetic workload login belonging to this Rookery installation.
func (p Project) BootstrapOwnedIndexProbe(ctx context.Context, token, password string) error {
	role := p.OwnedRepairProbeRole("index")
	if role == "" {
		return ErrSetupPermission
	}
	return p.bootstrap(ctx, token, password, role, true)
}

// OwnedRepairProbeRole names a dedicated writer for one owned staging installation.
func (p Project) OwnedRepairProbeRole(operation string) string {
	if p.Ref() != OwnedIndexProbeRef || p.key == "" {
		return ""
	}
	var prefix string
	switch operation {
	case "index":
		prefix = "pt_hosted_ix_"
	case "analyze", "statistics":
		prefix = "pt_hosted_st_"
	case "vacuum":
		prefix = "pt_hosted_vc_"
	case "memory":
		prefix = "pt_hosted_fn_"
	case "cron":
		prefix = "pt_hosted_cr_"
	default:
		return ""
	}
	return prefix + p.key
}

func (p Project) BootstrapOwnedRepairProbe(ctx context.Context, token, password, operation string) error {
	role := p.OwnedRepairProbeRole(operation)
	if role == "" || operation == "index" {
		return ErrSetupPermission
	}
	return p.bootstrap(ctx, token, password, role, false)
}

func (p Project) bootstrap(ctx context.Context, token, password, discoveryRole string, installIndexReader bool) error {
	if p.ref == "" || p.key == "" || len(token) == 0 || len(token) > 16384 ||
		strings.ContainsAny(token, " \t\r\n") || password == "" {
		return ErrSetupPermission
	}
	script, err := p.bootstrapSQLForRole(password, discoveryRole, installIndexReader)
	if err != nil {
		return err
	}
	return p.postSetup(ctx, setupHTTP, token, script)
}

// Reconnect renews the managed monitor only when both ownership markers match.
func (p Project) Reconnect(ctx context.Context, token, password string) error {
	if p.ref == "" || p.key == "" || len(token) == 0 || len(token) > 16384 ||
		strings.ContainsAny(token, " \t\r\n") || password == "" {
		return ErrSetupPermission
	}
	verifier, err := passwordVerifier(password)
	if err != nil {
		return ErrSetupUnavailable
	}
	return p.postSetup(ctx, setupHTTP, token, p.reconnectSQL(verifier))
}

func (p Project) reconnectSQL(verifier string) string {
	return fmt.Sprintf(`BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '2s';
DO $reconnect$
BEGIN
    IF NOT EXISTS (
        SELECT FROM pg_roles r JOIN pg_namespace n ON n.nspname=r.rolname
        WHERE r.rolname='%[1]s'
          AND obj_description(n.oid, 'pg_namespace')='%[2]s'
          AND shobj_description(r.oid, 'pg_authid')='%[2]s'
          AND NOT (r.rolsuper OR r.rolinherit OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication)
          AND NOT EXISTS (SELECT FROM pg_auth_members m WHERE m.member=r.oid)
          AND r.rolconnlimit BETWEEN 1 AND 2
    ) THEN
        RAISE EXCEPTION 'the managed monitor installation could not be verified';
    END IF;
    ALTER ROLE %[1]s LOGIN PASSWORD '%[3]s';
END
$reconnect$;
COMMIT;`, p.Role(), p.bootstrapMarker(), verifier)
}

func (p Project) bootstrapMarker() string { return "Pooltune managed monitor v1 " + p.ID() }

func (p Project) postSetup(ctx context.Context, client *http.Client, token, script string) error {
	body, err := json.Marshal(struct {
		Query string `json:"query"`
	}{Query: script})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.supabase.com/v1/projects/"+p.Ref()+"/database/query", bytes.NewReader(body))
	if err != nil {
		return ErrSetupUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return ErrSetupUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrSetupPermission
	default:
		return ErrSetupUnavailable
	}
}

func (p Project) bootstrapSQL(password string) (string, error) {
	return p.bootstrapSQLWithDiscovery(password, false)
}

func (p Project) bootstrapSQLWithDiscovery(password string, ownedProbe bool) (string, error) {
	if !ownedProbe {
		return p.bootstrapSQLForRole(password, "", false)
	}
	role := p.OwnedRepairProbeRole("index")
	if role == "" {
		return "", ErrSetupPermission
	}
	return p.bootstrapSQLForRole(password, role, true)
}

func (p Project) bootstrapSQLForRole(password, discoveryRole string, installIndexReader bool) (string, error) {
	discovery := p.AutomaticDiscoverySQL()
	installers := []string{p.InstallSQL(), discovery, p.discoveredLookupSQL()}
	if discoveryRole != "" {
		objective := "CASE WHEN a.userid = to_regrole('" + discoveryRole + "')::oid THEN 2.0 ELSE 1000.0 END"
		installers[1] = strings.Replace(discovery, "/* POOLTUNE_DISCOVERY_OBJECTIVE */ 1000", objective, 1)
	}
	if installIndexReader {
		installers = append(installers, p.DiscoveredIndexEvidenceSQL())
	}
	var script strings.Builder
	script.WriteString("BEGIN;\n")
	for _, installer := range installers {
		body, ok := strings.CutPrefix(strings.TrimSpace(installer), "BEGIN;")
		if !ok {
			return "", ErrSetupUnavailable
		}
		body, ok = strings.CutSuffix(body, "COMMIT;")
		if !ok {
			return "", ErrSetupUnavailable
		}
		script.WriteString(body)
		script.WriteByte('\n')
	}
	verifier, err := passwordVerifier(password)
	if err != nil {
		return "", ErrSetupUnavailable
	}
	fmt.Fprintf(&script, "COMMENT ON ROLE %[1]s IS '%[2]s';\nCOMMENT ON SCHEMA %[1]s IS '%[2]s';\n", p.Role(), p.bootstrapMarker())
	script.WriteString("ALTER ROLE " + p.Role() + " LOGIN PASSWORD '" + verifier + "';\nCOMMIT;\n")
	return script.String(), nil
}

func passwordVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, 4096, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(label string) []byte {
		h := hmac.New(sha256.New, salted)
		h.Write([]byte(label))
		return h.Sum(nil)
	}
	client := sha256.Sum256(mac("Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$4096:" + b64(salt) + "$" + b64(client[:]) + ":" + b64(mac("Server Key")), nil
}

// ConfigureDiscoveryObjective applies the owner's mean-latency goal to the
// normal automatic workload reader, including an existing installation.
func (p Project) ConfigureDiscoveryObjective(ctx context.Context, token string, objectiveMS int) error {
	if p.ref == "" || p.key == "" || token == "" || len(token) > 16384 || strings.ContainsAny(token, " \t\r\n") {
		return ErrSetupPermission
	}
	script, err := p.automaticObjectiveSQL(objectiveMS)
	if err != nil {
		return err
	}
	return p.postSetup(ctx, setupHTTP, token, script)
}

func (p Project) automaticObjectiveSQL(objectiveMS int) (string, error) {
	if objectiveMS < 1 || objectiveMS > 1000 {
		return "", errors.New("choose a mean query latency goal between 1 and 1000 milliseconds")
	}
	script := strings.Replace(p.AutomaticDiscoverySQL(), "BEGIN;", p.managedInstallationGuardSQL(), 1)
	const marker = "/* POOLTUNE_DISCOVERY_OBJECTIVE */ 1000"
	if strings.Count(script, marker) != 1 {
		return "", ErrSetupUnavailable
	}
	return strings.Replace(script, marker, "/* POOLTUNE_DISCOVERY_OBJECTIVE */ "+fmt.Sprint(objectiveMS), 1), nil
}
