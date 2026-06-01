// Package unifi is a minimal client for the UniFi OS local API (UDM / Cloud Key
// Gen2+ / UDR), used to create and remove WAN port-forward rules for game
// servers. Auth: POST /api/auth/login sets a TOKEN cookie and returns an
// X-CSRF-Token that must accompany mutating requests; the Network API lives
// under /proxy/network/api/s/<site>/...
package unifi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

type Client struct {
	base string
	user string
	pass string
	site string
	hc   *http.Client
	csrf string
}

// PortForward is the subset of a UniFi port-forward rule we manage.
type PortForward struct {
	ID            string `json:"_id,omitempty"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Src           string `json:"src"`            // "any"
	DstPort       string `json:"dst_port"`       // external port(s)
	Fwd           string `json:"fwd"`            // internal IP
	FwdPort       string `json:"fwd_port"`       // internal port
	Proto         string `json:"proto"`          // tcp | udp | tcp_udp
	PfwdInterface string `json:"pfwd_interface"` // "wan"
	Log           bool   `json:"log"`
}

// New builds a client. baseURL like https://192.168.1.1 ; site usually "default".
func New(baseURL, user, pass, site string) *Client {
	if site == "" {
		site = "default"
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		user: user, pass: pass, site: site,
		hc: &http.Client{
			Timeout: 12 * time.Second,
			Jar:     jar,
			// UniFi OS uses a self-signed cert on the local API.
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
	}
}

// Login authenticates and captures the CSRF token for later mutations.
func (c *Client) Login() error {
	body, _ := json.Marshal(map[string]string{"username": c.user, "password": c.pass})
	req, _ := http.NewRequest("POST", c.base+"/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != 200 {
		return fmt.Errorf("login failed (HTTP %d) — check URL/credentials", resp.StatusCode)
	}
	c.csrf = resp.Header.Get("X-CSRF-Token")
	return nil
}

func (c *Client) netURL(path string) string {
	return fmt.Sprintf("%s/proxy/network/api/s/%s%s", c.base, c.site, path)
}

func (c *Client) do(method, url string, in any) ([]byte, error) {
	var rdr io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rdr)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// UniFi rotates the CSRF token on some responses.
	if t := resp.Header.Get("X-Updated-Csrf-Token"); t != "" {
		c.csrf = t
	} else if t := resp.Header.Get("X-CSRF-Token"); t != "" {
		c.csrf = t
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return b, fmt.Errorf("unifi %s %s: HTTP %d", method, url, resp.StatusCode)
	}
	return b, nil
}

// ListPortForwards returns all port-forward rules.
func (c *Client) ListPortForwards() ([]PortForward, error) {
	b, err := c.do("GET", c.netURL("/rest/portforward"), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []PortForward `json:"data"`
	}
	json.Unmarshal(b, &out)
	return out.Data, nil
}

// CreatePortForward adds a rule.
func (c *Client) CreatePortForward(pf PortForward) error {
	if pf.Src == "" {
		pf.Src = "any"
	}
	if pf.PfwdInterface == "" {
		pf.PfwdInterface = "wan"
	}
	_, err := c.do("POST", c.netURL("/rest/portforward"), pf)
	return err
}

// DeletePortForward removes a rule by id.
func (c *Client) DeletePortForward(id string) error {
	_, err := c.do("DELETE", c.netURL("/rest/portforward/"+id), nil)
	return err
}
