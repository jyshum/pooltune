package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"pooltune/internal/gateway"
)

// Client carries only the customer installation token. It has no provider key.
type Client struct {
	endpoint string
	token    func() (string, error)
	http     *http.Client
}

func NewClient(endpoint string, token func() (string, error), timeout time.Duration, localTest bool) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || token == nil || timeout <= 0 || timeout > 2*time.Minute {
		return nil, ErrInvalid
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !localTest || u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, ErrInvalid
		}
	}
	return &Client{endpoint: endpoint, token: token, http: &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, MaxResponseHeaderBytes: 16384}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) post(ctx context.Context, path string, input, output any) error {
	body, e := json.Marshal(input)
	if e != nil || len(body) > 128<<10 {
		return ErrInvalid
	}
	token, e := c.token()
	if e != nil || len(token) != 43 {
		return ErrDenied
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.endpoint+path, bytes.NewReader(body))
	if e != nil {
		return ErrInvalid
	}
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, e := c.http.Do(req)
	if e != nil {
		return ErrDenied
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if e != nil || len(raw) > 128<<10 || response.StatusCode != 200 {
		return ErrDenied
	}
	return decode(raw, output)
}
func (c *Client) Start(ctx context.Context) (string, error) {
	var out struct {
		Run string `json:"run"`
	}
	if e := c.post(ctx, "/v1/runs", struct{}{}, &out); e != nil {
		return "", e
	}
	if len(out.Run) != 43 {
		return "", ErrInvalid
	}
	return out.Run, nil
}
func (c *Client) Step(ctx context.Context, run string, step int, in gateway.ModelRequest) (gateway.ModelResponse, error) {
	var out gateway.ModelResponse
	e := c.post(ctx, "/v1/step", gateway.StepRequest{Run: run, Step: step, ModelRequest: in}, &out)
	return out, e
}
