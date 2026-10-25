package provider

import (
	"context"
	"encoding/json"
	"net/url"
)

func (a *Adapter) supabaseObserve(ctx context.Context) (map[string]int, map[string]any, error) {
	raw, _, e := a.request(ctx, "GET", a.cfg.Endpoint+"/v1/projects/"+url.PathEscape(a.cfg.Project)+"/config/database/pooler", nil)
	if e != nil {
		return nil, nil, e
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, nil, ErrAPI
	}
	if list, ok := value.([]any); ok {
		if len(list) != 1 {
			return nil, nil, ErrUnsupported
		}
		value = list[0]
	}
	pool, e := object(value)
	if e != nil || pool["pool_mode"] != "transaction" {
		return nil, nil, ErrUnsupported
	}
	size, e := number(pool["default_pool_size"])
	if e != nil || size < 1 {
		return nil, nil, ErrAPI
	}
	return map[string]int{"default_pool_size": size}, map[string]any{"default_pool_size": size, "pool_mode": "transaction"}, nil
}
