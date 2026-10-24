package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const maxConfigBytes = 1 << 20

// Duration is a JSON string accepted by time.ParseDuration, such as "10s".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return errors.New("duration must be a string")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return errors.New("invalid duration")
	}
	*d = Duration(v)
	return nil
}

func (p *EvaluationProfile) UnmarshalJSON(data []byte) error {
	var wire struct {
		Version                string   `json:"version"`
		MinWindow              Duration `json:"min_window"`
		MaxWindow              Duration `json:"max_window"`
		MinSamplesPerWorkload  int      `json:"min_samples_per_workload"`
		MaxFailureRateIncrease float64  `json:"max_failure_rate_increase"`
		MaxLatencyRatio        float64  `json:"max_latency_ratio"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return errors.New("invalid evaluation profile")
	}
	*p = EvaluationProfile{
		Version:                wire.Version,
		MinWindow:              time.Duration(wire.MinWindow),
		MaxWindow:              time.Duration(wire.MaxWindow),
		MinSamplesPerWorkload:  wire.MinSamplesPerWorkload,
		MaxFailureRateIncrease: wire.MaxFailureRateIncrease,
		MaxLatencyRatio:        wire.MaxLatencyRatio,
	}
	return nil
}

type Config struct {
	SchemaVersion     string            `json:"schema_version"`
	AutomationEnabled bool              `json:"automation_enabled"`
	Targets           []Target          `json:"targets"`
	EvaluationProfile EvaluationProfile `json:"evaluation_profile"`
	ModelProfile      ModelProfile      `json:"model_profile"`
	SecretRefs        SecretRefs        `json:"secret_refs"`
	Gateway           *GatewayConfig    `json:"gateway"`
}

type Target struct {
	ID         string         `json:"id"`
	Owner      string         `json:"owner"`
	Postgres   PostgresLimits `json:"postgres"`
	REST       RESTLimits     `json:"rest"`
	Guardrails Guardrails     `json:"guardrails"`
}

type PostgresLimits struct {
	AdmissionFloorTransactions   int `json:"admission_floor_transactions"`
	AdmissionStartTransactions   int `json:"admission_start_transactions"`
	AdmissionCeilingTransactions int `json:"admission_ceiling_transactions"`
	PhysicalCeilingConnections   int `json:"physical_ceiling_connections"`
	CriticalReserveTransactions  int `json:"critical_reserve_transactions"`
}

type RESTLimits struct {
	MaxInFlightRequests           int `json:"max_in_flight_requests"`
	PhysicalAllocationConnections int `json:"physical_allocation_connections"`
	CriticalReserveRequests       int `json:"critical_reserve_requests"`
}

type Thresholds struct {
	Grow    float64 `json:"grow"`
	Backoff float64 `json:"backoff"`
	Stop    float64 `json:"stop"`
}

type MetricMaxAge struct {
	CPU         Duration `json:"cpu"`
	Memory      Duration `json:"memory"`
	Capacity    Duration `json:"capacity"`
	Connections Duration `json:"connections"`
}

type MaxOccupancy struct {
	Critical   Duration `json:"critical"`
	Normal     Duration `json:"normal"`
	Background Duration `json:"background"`
}

type Guardrails struct {
	Version      string       `json:"version"`
	CPU          Thresholds   `json:"cpu"`
	Memory       Thresholds   `json:"memory"`
	MetricMaxAge MetricMaxAge `json:"metric_max_age"`
	MaxOccupancy MaxOccupancy `json:"max_occupancy"`
}

type ModelProfile struct {
	GatewayProfileID string `json:"gateway_profile_id"`
}

type SecretRefs struct {
	PostgresCredentialFile string `json:"postgres_credential_file"`
	InstallationTokenFile  string `json:"installation_token_file"`
}

// GatewayConfig belongs only to the owned gateway process.
type GatewayConfig struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	ProviderAPIKeyFile string `json:"provider_api_key_file"`
}

// LoadConfig reads at most 1 MiB, requires exact JSON field spelling, and rejects
// duplicate keys at every level. It never reads any referenced secret file.
func LoadConfig(r io.Reader) (Config, error) {
	var c Config
	data, err := io.ReadAll(io.LimitReader(r, maxConfigBytes+1))
	if err != nil {
		return c, errors.New("cannot read config")
	}
	if len(data) > maxConfigBytes {
		return c, errors.New("config exceeds 1 MiB")
	}
	if err := checkKeys(data, reflect.TypeOf(c)); err != nil {
		return c, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, errors.New("invalid config JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("trailing config JSON")
	}
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func checkKeys(data []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Slice {
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return errors.New("invalid config JSON")
		}
		for _, item := range items {
			if err := checkKeys(item, typ.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	fields := make(map[string]reflect.Type, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		fields[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Type
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("invalid config object")
	}
	seen := map[string]bool{}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return errors.New("invalid config JSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("invalid config JSON")
		}
		fieldType, ok := fields[key]
		if !ok {
			return fmt.Errorf("unknown config field %q", key)
		}
		if seen[key] {
			return fmt.Errorf("duplicate config field %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return errors.New("invalid config JSON")
		}
		if err := checkKeys(value, fieldType); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("invalid config JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing config JSON")
	}
	return nil
}

func validThresholds(t Thresholds) bool {
	return finite(t.Grow) && finite(t.Backoff) && finite(t.Stop) &&
		t.Grow > 0 && t.Grow < t.Backoff && t.Backoff < t.Stop && t.Stop <= 1
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func validPostgres(p PostgresLimits) bool {
	return p.AdmissionFloorTransactions > 0 &&
		p.AdmissionStartTransactions >= p.AdmissionFloorTransactions &&
		p.AdmissionCeilingTransactions >= p.AdmissionStartTransactions &&
		p.PhysicalCeilingConnections > 0 &&
		p.AdmissionCeilingTransactions <= p.PhysicalCeilingConnections &&
		p.CriticalReserveTransactions >= 0 &&
		p.CriticalReserveTransactions <= p.AdmissionCeilingTransactions
}

func validREST(r RESTLimits) bool {
	return r.MaxInFlightRequests > 0 && r.PhysicalAllocationConnections > 0 &&
		r.CriticalReserveRequests >= 0 && r.CriticalReserveRequests <= r.MaxInFlightRequests
}

func validGuardrails(g Guardrails) bool {
	return g.Version == "v1" && validThresholds(g.CPU) && validThresholds(g.Memory) &&
		g.MetricMaxAge.CPU > 0 && g.MetricMaxAge.Memory > 0 &&
		g.MetricMaxAge.Capacity > 0 && g.MetricMaxAge.Connections > 0 &&
		g.MaxOccupancy.Critical > 0 && g.MaxOccupancy.Normal > 0 && g.MaxOccupancy.Background > 0
}

func Validate(c Config) error {
	if c.SchemaVersion != "v1" {
		return errors.New("unsupported config schema version")
	}
	if c.Gateway != nil {
		if len(c.Targets) != 0 || c.AutomationEnabled || c.ModelProfile != (ModelProfile{}) || c.SecretRefs != (SecretRefs{}) || c.EvaluationProfile != (EvaluationProfile{}) {
			return errors.New("gateway and customer settings cannot be combined")
		}
		if c.Gateway.Provider != "anthropic" || c.Gateway.Model == "" || !filepath.IsAbs(c.Gateway.ProviderAPIKeyFile) {
			return errors.New("invalid gateway profile or provider key reference")
		}
		return nil
	}
	if len(c.Targets) == 0 {
		return errors.New("at least one target is required")
	}
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if t.ID == "" || t.Owner == "" {
			return errors.New("target ID and owner are required")
		}
		if seen[t.ID] {
			return errors.New("duplicate target ownership")
		}
		seen[t.ID] = true
		if !validPostgres(t.Postgres) {
			return errors.New("invalid PostgreSQL limits")
		}
		if !validREST(t.REST) {
			return errors.New("invalid REST limits")
		}
		if !validGuardrails(t.Guardrails) {
			return errors.New("missing or invalid guardrails")
		}
	}
	if c.AutomationEnabled {
		if c.EvaluationProfile.Version != "v1" || !c.EvaluationProfile.Valid() {
			return errors.New("missing or invalid evaluation profile")
		}
		if c.ModelProfile.GatewayProfileID == "" {
			return errors.New("missing model profile")
		}
		if !filepath.IsAbs(c.SecretRefs.PostgresCredentialFile) || !filepath.IsAbs(c.SecretRefs.InstallationTokenFile) {
			return errors.New("missing secret file references")
		}
	}
	return nil
}

func ValidateForMode(c Config, mode string) error {
	if err := Validate(c); err != nil {
		return err
	}
	switch mode {
	case "gateway":
		if c.Gateway == nil {
			return errors.New("gateway mode requires gateway config")
		}
	case "traffic", "control":
		if c.Gateway != nil {
			return errors.New("provider key config cannot run in customer mode")
		}
	default:
		return errors.New("unsupported mode")
	}
	return nil
}
