// Package npm is a minimal client for the Nginx Proxy Manager (jc21) API, used
// to create and remove proxy hosts that route a subdomain to a game/app server's
// published host port. Auth: POST /api/tokens {identity,secret} → {token}; the
// token is sent as a Bearer header on subsequent requests and cached for the
// lifetime of the client. Mirrors internal/unifi in shape.
package npm

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base  string
	email string
	pass  string
	hc    *http.Client

	token string
}

// ProxyHost is the subset of an NPM proxy-host we manage.
type ProxyHost struct {
	ID          int      `json:"id,omitempty"`
	DomainNames []string `json:"domain_names"`
	ForwardHost string   `json:"forward_host"`
	ForwardPort int      `json:"forward_port"`
}

// CreateOpts tunes a created proxy host. Zero value = sensible defaults
// (request a fresh Let's Encrypt cert, force SSL, websocket + http2 on).
type CreateOpts struct {
	LEEmail string // Let's Encrypt account email
	NoSSL   bool   // true = certificate_id 0 (plain http, no cert request)
}

// New builds a client. baseURL like http://192.168.1.158:81 (the NPM admin API).
func New(baseURL, email, pass string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" && !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		base:  baseURL,
		email: email,
		pass:  pass,
		hc: &http.Client{
			Timeout: 15 * time.Second,
			// NPM may sit behind a self-signed https endpoint.
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
	}
}

// Login obtains an API token. NPM tokens last ~1 day; we cache and refresh.
func (c *Client) Login() error {
	body, _ := json.Marshal(map[string]string{"identity": c.email, "secret": c.pass})
	req, _ := http.NewRequest("POST", c.base+"/api/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return fmt.Errorf("login failed (HTTP %d) — check URL/credentials", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Token == "" {
		return fmt.Errorf("login: no token in response")
	}
	c.token = out.Token
	return nil
}

// ensureToken logs in if we have no token yet (callers Login() explicitly, so
// this mainly guards re-use of a long-lived client).
func (c *Client) ensureToken() error {
	if c.token == "" {
		return c.Login()
	}
	return nil
}

func (c *Client) do(method, path string, in any) ([]byte, error) {
	if err := c.ensureToken(); err != nil {
		return nil, err
	}
	var rdr io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rdr)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return b, fmt.Errorf("npm %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

// ListProxyHosts returns all proxy hosts (used to reconcile / avoid duplicates).
func (c *Client) ListProxyHosts() ([]ProxyHost, error) {
	b, err := c.do("GET", "/api/nginx/proxy-hosts", nil)
	if err != nil {
		return nil, err
	}
	var out []ProxyHost
	json.Unmarshal(b, &out)
	return out, nil
}

// CreateProxyHost adds a proxy host routing domain → fwdHost:fwdPort over http,
// and returns its NPM id.
func (c *Client) CreateProxyHost(domain, fwdHost string, fwdPort int, opts CreateOpts) (int, error) {
	certID := any("new")
	if opts.NoSSL {
		certID = 0
	}
	payload := map[string]any{
		"domain_names":            []string{domain},
		"forward_scheme":          "http",
		"forward_host":            fwdHost,
		"forward_port":            fwdPort,
		"access_list_id":          0,
		"certificate_id":          certID,
		"ssl_forced":              !opts.NoSSL,
		"block_exploits":          true,
		"allow_websocket_upgrade": true,
		"http2_support":           !opts.NoSSL,
		"hsts_enabled":            false,
		"caching_enabled":         false,
		"locations":               []any{},
		"advanced_config":         "",
		"meta": map[string]any{
			"letsencrypt_agree": !opts.NoSSL,
			"dns_challenge":     false,
			"letsencrypt_email": opts.LEEmail,
		},
	}
	b, err := c.do("POST", "/api/nginx/proxy-hosts", payload)
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int `json:"id"`
	}
	json.Unmarshal(b, &out)
	return out.ID, nil
}

// DeleteProxyHost removes a proxy host by id.
func (c *Client) DeleteProxyHost(id int) error {
	_, err := c.do("DELETE", fmt.Sprintf("/api/nginx/proxy-hosts/%d", id), nil)
	return err
}
