package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Azure Blob Storage, through its REST API, authenticated with the storage
// account's access key or with a SAS token.
//
// An archive goes up as a block blob: one Put Block per chunk while the stream
// is read, then one Put Block List that turns them into the blob. Nothing exists
// under the name until that last call, so a backup that fails halfway leaves no
// half-written archive behind (Azure discards uncommitted blocks after a week).

const (
	azureVersion   = "2021-08-06"
	azureBlockSize = 16 << 20 // 50 000 blocks of 16 MiB: 800 GB per archive; one block is held in memory
)

type azureTarget struct {
	base      *url.URL // https://<account>.blob.core.windows.net/<container>
	account   string
	key       []byte     // shared-key auth; nil when sas is set
	sas       url.Values // SAS token auth
	container string
	prefix    string
	hc        *http.Client
	block     int
}

func openAzure(cfg Config) (Target, error) {
	account := strings.TrimSpace(cfg.Username)
	if account == "" {
		return nil, errors.New("azure: storage account name is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("azure: container is required")
	}
	secret := strings.TrimSpace(cfg.Password)
	if secret == "" {
		return nil, errors.New("azure: an access key or a SAS token is required")
	}
	t := &azureTarget{account: account, container: cfg.Bucket, prefix: cleanPrefix(cfg.Path), hc: &http.Client{}, block: azureBlockSize}

	if sas, ok := parseSAS(secret); ok {
		t.sas = sas
	} else {
		k, err := base64.StdEncoding.DecodeString(secret)
		if err != nil {
			return nil, errors.New("azure: that is neither an access key (base64, under Security + networking → Access keys) nor a SAS token (starts with sv=)")
		}
		t.key = k
	}

	// Default endpoint is the public cloud's. A Host overrides it, for Azure's
	// sovereign clouds or for the Azurite emulator, whose addresses carry the
	// account name in the path (http://127.0.0.1:10000/devstoreaccount1).
	endpoint := strings.TrimSpace(cfg.Host)
	if endpoint == "" {
		endpoint = "https://" + account + ".blob.core.windows.net"
	} else if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("azure: endpoint must be an address like https://<account>.blob.core.windows.net")
	}
	u.Path += "/" + cfg.Bucket
	t.base = u
	return t, nil
}

// parseSAS recognises a SAS token, pasted with or without its leading "?" or as
// the whole SAS URL the portal shows.
func parseSAS(s string) (url.Values, bool) {
	if i := strings.Index(s, "?"); i >= 0 {
		s = s[i+1:]
	}
	q, err := url.ParseQuery(s)
	if err != nil || q.Get("sig") == "" || q.Get("sv") == "" {
		return nil, false
	}
	return q, true
}

func (t *azureTarget) blobURL(name string) *url.URL {
	u := *t.base
	key := t.prefix + strings.TrimLeft(name, "/")
	u.Path += "/" + key
	return &u
}

// do signs and sends one request. query is merged with the SAS token, if any.
func (t *azureTarget) do(ctx context.Context, method string, u *url.URL, query url.Values, body []byte, hdr map[string]string) (*http.Response, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	for k, v := range t.sas {
		q[k] = v
	}
	uu := *u
	uu.RawQuery = q.Encode()

	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, uu.String(), rd)
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	req.Header.Set("x-ms-version", azureVersion)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if t.key != nil {
		req.Header.Set("Authorization", "SharedKey "+t.account+":"+t.sign(req, query))
	}
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, azureErr(resp, t.sas != nil)
	}
	return resp, nil
}

// sign computes the Shared Key signature. query is the request's own
// parameters; a SAS is never combined with a key, so it is not among them.
func (t *azureTarget) sign(req *http.Request, query url.Values) string {
	h := req.Header
	length := ""
	if req.ContentLength > 0 {
		length = strconv.FormatInt(req.ContentLength, 10)
	}
	var ms []string
	for k := range h {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-ms-") {
			ms = append(ms, lk)
		}
	}
	sort.Strings(ms)
	var canonHdr strings.Builder
	for _, k := range ms {
		canonHdr.WriteString(k + ":" + strings.TrimSpace(h.Get(k)) + "\n")
	}
	var canonRes strings.Builder
	canonRes.WriteString("/" + t.account + req.URL.EscapedPath())
	var qk []string
	for k := range query {
		qk = append(qk, k)
	}
	sort.Strings(qk)
	for _, k := range qk {
		vals := append([]string(nil), query[k]...)
		sort.Strings(vals)
		canonRes.WriteString("\n" + strings.ToLower(k) + ":" + strings.Join(vals, ","))
	}
	toSign := strings.Join([]string{
		req.Method,
		h.Get("Content-Encoding"),
		h.Get("Content-Language"),
		length,
		h.Get("Content-MD5"),
		h.Get("Content-Type"),
		"", // Date: x-ms-date is used instead
		h.Get("If-Modified-Since"),
		h.Get("If-Match"),
		h.Get("If-None-Match"),
		h.Get("If-Unmodified-Since"),
		h.Get("Range"),
	}, "\n") + "\n" + canonHdr.String() + canonRes.String()
	mac := hmac.New(sha256.New, t.key)
	mac.Write([]byte(toSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func azureErr(resp *http.Response, sas bool) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	msg := resp.Status
	if xml.Unmarshal(body, &e) == nil && e.Code != "" {
		msg = e.Code
		if first, _, _ := strings.Cut(e.Message, "\n"); first != "" {
			msg += ": " + strings.TrimSpace(first)
		}
	}
	hint := ""
	switch e.Code {
	case "ContainerNotFound":
		hint = " — create the container first; the panel does not create it"
	case "AuthenticationFailed", "AuthorizationFailure":
		// Azure answers a wrong key with either code, and an under-privileged
		// SAS with the second; which one it is depends on what was sent.
		if sas {
			hint = " — check the SAS token: it needs read, write, delete and list permission on the container, and must not have expired"
		} else {
			hint = " — check the storage account name and its access key"
		}
	case "AuthorizationPermissionMismatch", "AuthorizationResourceTypeMismatch":
		hint = " — the SAS token needs read, write, delete and list permission on the container"
	}
	return &azureError{Status: resp.StatusCode, msg: fmt.Sprintf("azure: %d %s%s", resp.StatusCode, msg, hint)}
}

type azureError struct {
	Status int
	msg    string
}

func (e *azureError) Error() string { return e.msg }

func (t *azureTarget) Put(ctx context.Context, name string, r io.Reader) (int64, error) {
	u := t.blobURL(name)
	buf := make([]byte, t.block)
	var ids []string
	var total int64
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			// Block ids must all be the same length within a blob.
			id := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("ygg-%08d", len(ids))))
			if err := t.putBlock(ctx, u, id, buf[:n]); err != nil {
				return total, err
			}
			ids = append(ids, id)
			total += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return total, rerr
		}
	}
	var list bytes.Buffer
	list.WriteString(`<?xml version="1.0" encoding="utf-8"?><BlockList>`)
	for _, id := range ids {
		list.WriteString("<Latest>" + id + "</Latest>")
	}
	list.WriteString("</BlockList>")
	resp, err := t.do(ctx, "PUT", u, url.Values{"comp": {"blocklist"}}, list.Bytes(), map[string]string{
		"Content-Type":           "application/xml",
		"x-ms-blob-content-type": "application/gzip",
	})
	if err != nil {
		return total, err
	}
	resp.Body.Close()
	return total, nil
}

// putBlock uploads one block, retrying a failure on the way: the block is in
// memory, so a retry costs nothing but time.
func (t *azureTarget) putBlock(ctx context.Context, u *url.URL, id string, data []byte) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		resp, err := t.do(ctx, "PUT", u, url.Values{"comp": {"block"}, "blockid": {id}}, data, nil)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		lastErr = err
		var ae *azureError
		if errors.As(err, &ae) && ae.Status < 500 {
			return err // a refusal, not a hiccup
		}
	}
	return lastErr
}

func (t *azureTarget) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := t.do(ctx, "GET", t.blobURL(name), nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (t *azureTarget) List(ctx context.Context) ([]Object, error) {
	var objs []Object
	marker := ""
	for {
		q := url.Values{"restype": {"container"}, "comp": {"list"}, "delimiter": {"/"}}
		if t.prefix != "" {
			q.Set("prefix", t.prefix)
		}
		if marker != "" {
			q.Set("marker", marker)
		}
		resp, err := t.do(ctx, "GET", t.base, q, nil, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Blobs []struct {
				Name       string `xml:"Name"`
				Properties struct {
					Length   int64  `xml:"Content-Length"`
					Modified string `xml:"Last-Modified"`
				} `xml:"Properties"`
			} `xml:"Blobs>Blob"`
			NextMarker string `xml:"NextMarker"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("azure: list: %w", err)
		}
		for _, b := range page.Blobs {
			mod, _ := time.Parse(time.RFC1123, b.Properties.Modified)
			objs = append(objs, Object{Name: strings.TrimPrefix(b.Name, t.prefix), Size: b.Properties.Length, ModTime: mod})
		}
		if page.NextMarker == "" {
			return objs, nil
		}
		marker = page.NextMarker
	}
}

func (t *azureTarget) Delete(ctx context.Context, name string) error {
	resp, err := t.do(ctx, "DELETE", t.blobURL(name), nil, nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *azureTarget) Close() error { return nil }
