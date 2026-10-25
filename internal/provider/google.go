package provider

import (
	"context"
	"net/url"
	"strconv"
)

func stringInt(v int) string { return strconv.Itoa(v) }
func (a *Adapter) googleBase() string {
	return a.cfg.Endpoint + "/v1/projects/" + url.PathEscape(a.cfg.Project)
}
func (a *Adapter) googleObserve(ctx context.Context) (map[string]int, map[string]any, error) {
	raw, _, e := a.request(ctx, "GET", a.googleBase()+"/instances/"+url.PathEscape(a.cfg.Instance), nil)
	if e != nil {
		return nil, nil, e
	}
	payload, e := decode(raw)
	if e != nil {
		return nil, nil, e
	}
	settings, e := object(payload["settings"])
	if e != nil {
		return nil, nil, e
	}
	pool, e := object(settings["connectionPoolConfig"])
	if e != nil || settings["edition"] != "ENTERPRISE_PLUS" || pool["connectionPoolingEnabled"] != true {
		return nil, nil, ErrUnsupported
	}
	version, ok := settings["settingsVersion"].(string)
	if !ok || version == "" {
		return nil, nil, ErrAPI
	}
	params := map[string]int{}
	seen := map[string]bool{}
	flags, ok := pool["flags"].([]any)
	if a.discovery && pool["flags"] == nil {
		flags = []any{}
		ok = true
	}
	if !ok || len(flags) > 128 {
		return nil, nil, ErrAPI
	}
	for _, flag := range flags {
		f, e := object(flag)
		if e != nil {
			return nil, nil, e
		}
		name, ok := f["name"].(string)
		if !ok || seen[name] {
			return nil, nil, ErrAPI
		}
		seen[name] = true
		if name == "max_pool_size" || name == "min_pool_size" {
			n, e := number(f["value"])
			if e != nil {
				return nil, nil, e
			}
			params[name] = n
		}
	}
	// Absent implicit defaults are discovery-incomplete, never invented capacity.
	max, hasMax := params["max_pool_size"]
	min, hasMin := params["min_pool_size"]
	if !hasMax || !hasMin || max < 1 || min > max {
		if a.discovery && (!hasMax || !hasMin) && (!hasMax || max >= 1) && (!hasMax || !hasMin || min <= max) {
			return params, settings, nil
		}
		return nil, nil, ErrUnsupported
	}
	return params, settings, nil
}
func (a *Adapter) googleApply(ctx context.Context, c TargetChange, state map[string]any) (string, error) {
	pool, _ := object(state["connectionPoolConfig"])
	flags := pool["flags"].([]any)
	replacement := make([]any, 0, len(flags))
	for _, flag := range flags {
		f, _ := object(flag)
		copy := map[string]any{}
		for k, v := range f {
			copy[k] = v
		}
		if copy["name"] == c.Parameter {
			copy["value"] = stringInt(c.Value)
		}
		replacement = append(replacement, copy)
	}
	raw, _, e := a.request(ctx, "PATCH", a.googleBase()+"/instances/"+url.PathEscape(a.cfg.Instance), map[string]any{"settings": map[string]any{"settingsVersion": state["settingsVersion"], "connectionPoolConfig": map[string]any{"flags": replacement}}})
	if e != nil {
		return "", e
	}
	op, e := decode(raw)
	if e != nil || op["error"] != nil {
		return "", ErrUncertain
	}
	name, ok := op["name"].(string)
	if !ok || !identifier.MatchString(name) {
		return "", ErrUncertain
	}
	return name, nil
}
