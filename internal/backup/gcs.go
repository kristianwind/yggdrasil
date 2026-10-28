package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Google Cloud Storage, through its own JSON API and a service-account key.
//
// Not through GCS's S3-compatible endpoint: an archive is streamed while it is
// being made, so its size is unknown when the upload starts, and that endpoint
// has no way to take a stream of unknown length. The JSON API's resumable upload
// does: each chunk says where it starts, and only the last one says how long
// the whole object was.

const (
	gcsAPI       = "https://storage.googleapis.com"
	gcsScope     = "https://www.googleapis.com/auth/devstorage.read_write"
	gcsTokenURL  = "https://oauth2.googleapis.com/token"
	gcsChunkSize = 16 << 20 // must be a multiple of 256 KiB; one chunk is held in memory
)

type gcsTarget struct {
	api    string // base URL; the real API, or an emulator in tests
	bucket string
	prefix string
	token  func(context.Context) (string, error)
	hc     *http.Client
	chunk  int
}

// serviceAccountKey is the part of a downloaded JSON key the panel uses.
type serviceAccountKey struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func parseServiceAccountKey(raw string) (*serviceAccountKey, error) {
	var k serviceAccountKey
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &k); err != nil {
		return nil, errors.New("gcs: the key must be the JSON file Google gives you for a service account (IAM → Service accounts → Keys → Add key → JSON)")
	}
	if k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, errors.New("gcs: that JSON is not a service-account key — it needs type \"service_account\", client_email and private_key")
	}
	if k.TokenURI == "" {
		k.TokenURI = gcsTokenURL
	}
	return &k, nil
}

func openGCS(cfg Config) (Target, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("gcs: bucket is required")
	}
	key, err := parseServiceAccountKey(cfg.Password)
	if err != nil {
		return nil, err
	}
	signer, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(key.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("gcs: the key's private_key does not parse: %w", err)
	}
	// No client timeout: it would also cut off the body of a long restore
	// download. Every request carries the caller's context instead.
	hc := &http.Client{}
	ts := &gcsTokenSource{key: key, signer: signer, hc: hc}
	return &gcsTarget{api: gcsAPI, bucket: cfg.Bucket, prefix: cleanPrefix(cfg.Path), token: ts.Token, hc: hc, chunk: gcsChunkSize}, nil
}

// gcsTokenSource trades a signed JWT for an access token, and keeps it until
// shortly before it expires.
type gcsTokenSource struct {
	key    *serviceAccountKey
	signer any
	hc     *http.Client

	mu      sync.Mutex
	tok     string
	expires time.Time
}

func (s *gcsTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok != "" && time.Until(s.expires) > 2*time.Minute {
		return s.tok, nil
	}
	now := time.Now()
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":   s.key.ClientEmail,
		"scope": gcsScope,
		"aud":   s.key.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}).SignedString(s.signer)
	if err != nil {
		return "", fmt.Errorf("gcs: sign token request: %w", err)
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.key.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("gcs: token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gcs: Google refused the service-account key (%d): %s — the key may have been deleted or the account disabled", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", errors.New("gcs: token response had no access token")
	}
	s.tok, s.expires = tr.AccessToken, now.Add(time.Duration(tr.ExpiresIn)*time.Second)
	return s.tok, nil
}

func (t *gcsTarget) key(name string) string { return t.prefix + strings.TrimLeft(name, "/") }

func (t *gcsTarget) objectURL(name string) string {
	return fmt.Sprintf("%s/storage/v1/b/%s/o/%s", t.api, url.PathEscape(t.bucket), url.PathEscape(t.key(name)))
}

// do sends an authenticated request and turns a non-2xx answer into an error.
func (t *gcsTarget) do(ctx context.Context, method, rawurl string, body io.Reader, hdr map[string]string) (*http.Response, error) {
	tok, err := t.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawurl, body)
	if err != nil {
		return nil, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, gcsErr(resp)
	}
	return resp, nil
}

func gcsErr(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	msg := strings.TrimSpace(string(body))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	err := fmt.Errorf("gcs: %d %s", resp.StatusCode, msg)
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w — check the bucket name; the panel does not create it", err)
	case http.StatusForbidden:
		return fmt.Errorf("%w — give the service account the Storage Object Admin role on this bucket", err)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w — the service-account key was refused", err)
	}
	return err
}

func (t *gcsTarget) Put(ctx context.Context, name string, r io.Reader) (int64, error) {
	start := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s",
		t.api, url.PathEscape(t.bucket), url.QueryEscape(t.key(name)))
	resp, err := t.do(ctx, "POST", start, bytes.NewReader([]byte(`{"contentType":"application/gzip"}`)),
		map[string]string{"Content-Type": "application/json; charset=UTF-8", "X-Upload-Content-Type": "application/gzip"})
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	session := resp.Header.Get("Location")
	if session == "" {
		return 0, errors.New("gcs: resumable upload started without a session address")
	}

	buf := make([]byte, t.chunk)
	var off int64
	for {
		n, rerr := io.ReadFull(r, buf)
		last := rerr == io.EOF || rerr == io.ErrUnexpectedEOF
		if rerr != nil && !last {
			return off, rerr
		}
		var rng string
		switch {
		case !last:
			rng = fmt.Sprintf("bytes %d-%d/*", off, off+int64(n)-1)
		case n == 0:
			rng = fmt.Sprintf("bytes */%d", off)
		default:
			rng = fmt.Sprintf("bytes %d-%d/%d", off, off+int64(n)-1, off+int64(n))
		}
		if err := t.putChunk(ctx, session, buf[:n], rng, last); err != nil {
			return off, err
		}
		off += int64(n)
		if last {
			return off, nil
		}
	}
}

// putChunk sends one chunk to the upload session. The session address is itself
// the credential, so no token goes with it. A chunk is held in memory, so a
// failure on the way is retried.
func (t *gcsTarget) putChunk(ctx context.Context, session string, chunk []byte, rng string, last bool) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, "PUT", session, bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		req.ContentLength = int64(len(chunk))
		req.Header.Set("Content-Range", rng)
		resp, err := t.hc.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		code := resp.StatusCode
		switch {
		case code == 308 && !last:
			resp.Body.Close()
			return nil
		case (code == 200 || code == 201) && last:
			resp.Body.Close()
			return nil
		case code >= 500:
			lastErr = gcsErr(resp)
			resp.Body.Close()
			continue
		default:
			err := gcsErr(resp)
			resp.Body.Close()
			return err
		}
	}
	return lastErr
}

func (t *gcsTarget) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := t.do(ctx, "GET", t.objectURL(name)+"?alt=media", nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (t *gcsTarget) List(ctx context.Context) ([]Object, error) {
	var objs []Object
	pageToken := ""
	for {
		q := url.Values{"prefix": {t.prefix}, "delimiter": {"/"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		resp, err := t.do(ctx, "GET", fmt.Sprintf("%s/storage/v1/b/%s/o?%s", t.api, url.PathEscape(t.bucket), q.Encode()), nil, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []struct {
				Name    string `json:"name"`
				Size    string `json:"size"`
				Updated string `json:"updated"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("gcs: list: %w", err)
		}
		for _, it := range page.Items {
			size, _ := strconv.ParseInt(it.Size, 10, 64)
			mod, _ := time.Parse(time.RFC3339, it.Updated)
			objs = append(objs, Object{Name: strings.TrimPrefix(it.Name, t.prefix), Size: size, ModTime: mod})
		}
		if page.NextPageToken == "" {
			return objs, nil
		}
		pageToken = page.NextPageToken
	}
}

func (t *gcsTarget) Delete(ctx context.Context, name string) error {
	resp, err := t.do(ctx, "DELETE", t.objectURL(name), nil, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (t *gcsTarget) Close() error { return nil }
