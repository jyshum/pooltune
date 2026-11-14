package provider

import (
	"context"
	"net/url"
	"strings"
)

func (a *Adapter) azureBase() string {
	return a.cfg.Endpoint + "/subscriptions/" + url.PathEscape(a.cfg.Subscription) + "/resourceGroups/" + url.PathEscape(a.cfg.ResourceGroup) + "/providers/Microsoft.DBforPostgreSQL/flexibleServers/" + url.PathEscape(a.cfg.Instance)
}
func (a *Adapter) azureURL(p string) string {
	return a.azureBase() + "/configurations/" + url.PathEscape(p) + "?api-version=2025-08-01"
}
func (a *Adapter) azureObserve(ctx context.Context) (map[string]int, map[string]any, error) {
	params := map[string]int{}
	state := map[string]any{}
	for _, name := range []string{"pgbouncer.default_pool_size", "pgbouncer.min_pool_size"} {
		raw, _, err := a.request(ctx, "GET", a.azureURL(name), nil)
		if err != nil {
			return nil, nil, err
		}
		payload, err := decode(raw)
		if err != nil {
			return nil, nil, err
		}
		props, err := object(payload["properties"])
		if err != nil {
			return nil, nil, err
		}
		value, err := number(props["value"])
		if err != nil {
			return nil, nil, err
		}
		params[name] = value
		state[name] = props
	}
	if params["pgbouncer.default_pool_size"] < 1 || params["pgbouncer.min_pool_size"] > params["pgbouncer.default_pool_size"] {
		return nil, nil, ErrAPI
	}
	return params, state, nil
}
func (a *Adapter) validAzureOperation(endpoint string) bool {
	u, e := url.Parse(endpoint)
	base, _ := url.Parse(a.cfg.Endpoint)
	return e == nil && u.Scheme == base.Scheme && u.Host == base.Host && u.User == nil && u.Fragment == "" && strings.HasPrefix(u.Path, "/subscriptions/"+a.cfg.Subscription+"/") && !strings.Contains(u.Path, "/../")
}
func (a *Adapter) azureApply(ctx context.Context, c TargetChange) (string, error) {
	_, headers, e := a.request(ctx, "PATCH", a.azureURL(c.Parameter), map[string]any{"properties": map[string]string{"value": stringInt(c.Value), "source": "user-override"}})
	if e != nil {
		return "", e
	}
	op := headers.Get("Azure-AsyncOperation")
	if !a.validAzureOperation(op) {
		return "", ErrUncertain
	}
	return op, nil
}
