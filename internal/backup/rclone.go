package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"
)

// Consumer cloud storage — Jottacloud, Dropbox, Box, OneDrive, Google Drive,
// pCloud, Mega, WebDAV/Nextcloud — through rclone.
//
// None of these speak S3, each has its own API, and most hand out OAuth tokens
// that expire within the hour and are refreshed with a refresh token that may
// itself rotate. rclone already knows every one of them, and keeps up with their
// changes, so the panel runs it in a short-lived container (as it runs the PBS
// client) instead of carrying eight API clients of its own.
//
// The login lives in an rclone.conf the panel keeps encrypted in the target's
// config and copies into each container. When rclone refreshes a token it
// writes the new one into that file, so the file is copied back out after every
// run and saved if it changed. Without that, a refresh token that rotates would
// work for exactly one day.

// RcloneConfPath is where the config file sits inside the container.
const RcloneConfPath = "/ygg/rclone.conf"

// rcloneRemote is the name of the one remote in the config.
const rcloneRemote = "ygg"

// RcloneRunner runs one rclone invocation. conf is copied in before it starts;
// the config as rclone left it is returned, also on failure.
type RcloneRunner interface {
	RunRclone(ctx context.Context, conf []byte, argv []string, stdin io.Reader, stdout io.Writer) (newConf []byte, stderr string, err error)
}

// RcloneProviders are the services offered in the panel, with the rclone
// backend each one is. "custom" is a whole config section pasted as is.
var RcloneProviders = map[string]string{
	"jottacloud": "jottacloud",
	"dropbox":    "dropbox",
	"box":        "box",
	"onedrive":   "onedrive",
	"drive":      "drive",
	"pcloud":     "pcloud",
	"mega":       "mega",
	"webdav":     "webdav",
	"custom":     "",
}

type rcloneTarget struct {
	run    RcloneRunner
	base   string // "ygg:folder"
	onConf func([]byte)

	mu   *sync.Mutex // one run at a time per target: two refreshes racing can invalidate each other's token
	conf []byte
}

// OpenRclone opens an rclone target. onConf is called with the config whenever
// a run changed it (a refreshed token), so it can be stored. mu serialises runs
// against the same login; the caller keeps one per target.
func OpenRclone(cfg Config, run RcloneRunner, mu *sync.Mutex, onConf func([]byte)) (Target, error) {
	if run == nil {
		return nil, errors.New("rclone: docker is not available")
	}
	if strings.TrimSpace(cfg.RcloneConf) == "" {
		return nil, errors.New("rclone: this target has no login yet — edit it and enter the credentials again")
	}
	folder := strings.Trim(path.Clean("/"+strings.TrimSpace(cfg.Path)), "/")
	if folder == "." {
		folder = ""
	}
	return &rcloneTarget{run: run, base: rcloneRemote + ":" + folder, onConf: onConf, mu: mu, conf: []byte(cfg.RcloneConf)}, nil
}

func (t *rcloneTarget) remote(name string) string {
	name = strings.TrimLeft(name, "/")
	if strings.HasSuffix(t.base, ":") {
		return t.base + name
	}
	return t.base + "/" + name
}

func (t *rcloneTarget) exec(ctx context.Context, argv []string, stdin io.Reader, stdout io.Writer) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	newConf, stderr, err := t.run.RunRclone(ctx, t.conf, argv, stdin, stdout)
	if len(newConf) > 0 && !bytes.Equal(newConf, t.conf) {
		t.conf = newConf
		if t.onConf != nil {
			t.onConf(newConf)
		}
	}
	if err != nil {
		return rcloneErr(stderr, err)
	}
	return nil
}

// rcloneErr keeps rclone's own last line, which is where the reason is.
func rcloneErr(stderr string, err error) error {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	msg := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			msg = l
			break
		}
	}
	// Drop rclone's "2026/10/28 19:36:20 ERROR : " prefix.
	if i := strings.Index(msg, " : "); i >= 0 && i < 40 {
		msg = msg[i+3:]
	}
	if msg == "" {
		return fmt.Errorf("rclone: %w", err)
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "invalid_grant") || strings.Contains(low, "token expired") || strings.Contains(low, "unauthorized") || strings.Contains(low, "401"):
		return fmt.Errorf("rclone: %s — the login has expired or was revoked; edit the target and log in again", msg)
	case strings.Contains(low, "insufficient") || strings.Contains(low, "quota") || strings.Contains(low, "storage full"):
		return fmt.Errorf("rclone: %s — the account is out of space", msg)
	}
	return fmt.Errorf("rclone: %s", msg)
}

func (t *rcloneTarget) Put(ctx context.Context, name string, r io.Reader) (int64, error) {
	cr := &countingReader{r: r}
	// rcat streams to services that take an upload of unknown size; for the
	// rest (Jottacloud wants an MD5 up front, OneDrive a length) rclone buffers
	// the archive in a temporary file inside the container first.
	if err := t.exec(ctx, []string{"rcat", t.remote(name)}, cr, nil); err != nil {
		return cr.n, err
	}
	return cr.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (t *rcloneTarget) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	// Check it is there first, so a missing archive fails here with a reason
	// instead of as an empty stream halfway into a restore.
	if err := t.exec(ctx, []string{"lsjson", "--stat", t.remote(name)}, nil, io.Discard); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(t.exec(ctx, []string{"cat", t.remote(name)}, nil, pw))
	}()
	return pr, nil
}

func (t *rcloneTarget) List(ctx context.Context) ([]Object, error) {
	// mkdir first: a folder that does not exist yet is not an error for a target
	// that has never been written to, and the Test button lists.
	if err := t.exec(ctx, []string{"mkdir", t.base}, nil, nil); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := t.exec(ctx, []string{"lsjson", "--files-only", t.base}, nil, &out); err != nil {
		return nil, err
	}
	var items []struct {
		Name    string    `json:"Name"`
		Size    int64     `json:"Size"`
		ModTime time.Time `json:"ModTime"`
	}
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		return nil, fmt.Errorf("rclone: list: %w", err)
	}
	objs := make([]Object, 0, len(items))
	for _, it := range items {
		objs = append(objs, Object{Name: it.Name, Size: it.Size, ModTime: it.ModTime})
	}
	return objs, nil
}

func (t *rcloneTarget) Delete(ctx context.Context, name string) error {
	return t.exec(ctx, []string{"deletefile", t.remote(name)}, nil, nil)
}

func (t *rcloneTarget) Close() error { return nil }

// RcloneSetup turns what an admin entered into a working rclone.conf with one
// remote named "ygg". For the OAuth services that means running rclone's own
// configuration, which for Jottacloud exchanges the single-use personal login
// token for a refreshable one; questions it asks on the way (which drive, which
// device) are answered with rclone's default.
func RcloneSetup(ctx context.Context, run RcloneRunner, cfg Config) ([]byte, error) {
	if run == nil {
		return nil, errors.New("rclone: docker is not available")
	}
	secret := strings.TrimSpace(cfg.Password)
	if secret == "" {
		return nil, errors.New("rclone: enter the login for this service")
	}
	backend, ok := RcloneProviders[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("rclone: unknown service %q", cfg.Provider)
	}
	if cfg.Provider == "custom" {
		return customRcloneConf(secret)
	}

	args := []string{"config", "create", rcloneRemote, backend}
	switch cfg.Provider {
	case "jottacloud":
		args = append(args, "config_type=standard", "config_login_token="+secret)
	case "mega":
		if cfg.Username == "" {
			return nil, errors.New("rclone: Mega needs the account's e-mail address")
		}
		args = append(args, "user="+cfg.Username, "pass="+secret)
	case "webdav":
		if cfg.Host == "" {
			return nil, errors.New("rclone: WebDAV needs the server address")
		}
		vendor := cfg.Region // the WebDAV flavour rides in Region; empty = other
		if vendor == "" {
			vendor = "other"
		}
		args = append(args, "url="+cfg.Host, "vendor="+vendor, "user="+cfg.Username, "pass="+secret)
	default:
		// OAuth services: the token JSON printed by `rclone authorize`.
		if !strings.HasPrefix(secret, "{") || !strings.Contains(secret, "access_token") {
			return nil, fmt.Errorf("rclone: paste the token that `rclone authorize \"%s\"` prints — the part between the arrows, starting with {\"access_token\"", backend)
		}
		args = append(args, "token="+secret, "config_is_local=false")
	}
	args = append(args, "--non-interactive")

	conf := []byte{}
	for step := 0; step < 20; step++ {
		var out bytes.Buffer
		newConf, stderr, err := run.RunRclone(ctx, conf, args, nil, &out)
		if len(newConf) > 0 {
			conf = newConf
		}
		if err != nil {
			return nil, setupErr(cfg.Provider, stderr, err)
		}
		var q struct {
			State  string `json:"State"`
			Error  string `json:"Error"`
			Option *struct {
				Name     string `json:"Name"`
				Default  any    `json:"Default"`
				Examples []struct {
					Value string `json:"Value"`
				} `json:"Examples"`
			} `json:"Option"`
		}
		if s := strings.TrimSpace(out.String()); s != "" {
			if err := json.Unmarshal([]byte(s), &q); err != nil {
				return nil, fmt.Errorf("rclone: unexpected answer from config: %.200s", s)
			}
		}
		if q.Error != "" {
			return nil, setupErr(cfg.Provider, q.Error, errors.New(q.Error))
		}
		if q.State == "" || q.Option == nil {
			if !bytes.Contains(conf, []byte("["+rcloneRemote+"]")) {
				return nil, errors.New("rclone: configuration finished without a remote")
			}
			return conf, nil
		}
		answer := ""
		if q.Option.Default != nil {
			answer = fmt.Sprint(q.Option.Default)
		}
		if answer == "" && len(q.Option.Examples) > 0 {
			answer = q.Option.Examples[0].Value
		}
		args = []string{"config", "update", rcloneRemote, "--continue", "--state", q.State, "--result", answer, "--non-interactive"}
	}
	return nil, errors.New("rclone: configuration did not finish")
}

func setupErr(provider, stderr string, err error) error {
	e := rcloneErr(stderr, err)
	if provider == "jottacloud" {
		return fmt.Errorf("%w — a Jottacloud personal login token works once; make a new one under Settings → Security and paste that", e)
	}
	return e
}

// customRcloneConf takes one remote's section as `rclone config show` prints
// it, and renames it to the remote the panel uses.
func customRcloneConf(text string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("[" + rcloneRemote + "]\n")
	sections, keys := 0, 0
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		l := strings.TrimSpace(line)
		switch {
		case l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, ";"):
		case strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]"):
			sections++
		case strings.Contains(l, "="):
			keys++
			b.WriteString(l + "\n")
		default:
			return nil, fmt.Errorf("rclone: %q is not a config line (key = value)", l)
		}
	}
	if sections > 1 {
		return nil, errors.New("rclone: paste one remote's section, not the whole file")
	}
	if !strings.Contains(b.String(), "\ntype ") && !strings.Contains(b.String(), "\ntype=") {
		return nil, errors.New("rclone: the section needs a type = line")
	}
	return []byte(b.String()), nil
}
