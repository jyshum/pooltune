package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"pooltune/internal/domain"
	"time"
)

type Client struct{ http *http.Client }

func NewClient(socket string) (*Client, error) {
	if !filepath.IsAbs(socket) {
		return nil, ErrInvalid
	}
	return &Client{http: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}, MaxConnsPerHost: 4, MaxResponseHeaderBytes: 8192}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) call(ctx context.Context, method, path string, input, output any) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil || len(body) > 2<<20 {
			return ErrInvalid
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(body))
	if err != nil {
		return ErrInvalid
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return ErrRecovery
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrRecovery
	}
	if output != nil {
		return decode(data, output)
	}
	return nil
}
func (c *Client) Activate(ctx context.Context, e domain.ActivationEnvelope) (ActivationAck, error) {
	var ack ActivationAck
	err := c.call(ctx, "POST", "/activate", e, &ack)
	return ack, err
}
func (c *Client) Status(ctx context.Context, r domain.RevisionID) (ActivationAck, error) {
	var ack ActivationAck
	err := c.call(ctx, "GET", "/status?revision="+url.QueryEscape(string(r)), nil, &ack)
	return ack, err
}
func (c *Client) Recover(ctx context.Context) error {
	return c.call(ctx, "POST", "/recover", struct{}{}, nil)
}

func (c *Client) RecoverConfirmed(ctx context.Context, e domain.ActivationEnvelope) error {
	return c.call(ctx, "POST", "/recover", e, nil)
}
