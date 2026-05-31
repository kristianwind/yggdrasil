package backup

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"time"

	"github.com/hirochachacha/go-smb2"
)

// smbTarget speaks SMB2 natively (no host mount needed) for CIFS/SMB shares.
type smbTarget struct {
	conn    net.Conn
	session *smb2.Session
	share   *smb2.Share
	base    string
}

func openSMB(cfg Config) (Target, error) {
	port := cfg.Port
	if port == 0 {
		port = 445
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(port)), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("smb dial: %w", err)
	}
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{User: cfg.Username, Password: cfg.Password},
	}
	session, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smb session: %w", err)
	}
	share, err := session.Mount(cfg.Share)
	if err != nil {
		session.Logoff()
		conn.Close()
		return nil, fmt.Errorf("smb mount %q: %w", cfg.Share, err)
	}
	base := cfg.Path
	if base != "" {
		share.MkdirAll(base, 0755)
	}
	return &smbTarget{conn: conn, session: session, share: share, base: base}, nil
}

func (t *smbTarget) full(name string) string {
	if t.base == "" {
		return name
	}
	return path.Join(t.base, name)
}

func (t *smbTarget) Put(_ context.Context, name string, r io.Reader) (int64, error) {
	dest := t.full(name)
	if dir := path.Dir(dest); dir != "." {
		t.share.MkdirAll(dir, 0755)
	}
	f, err := t.share.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

func (t *smbTarget) Get(_ context.Context, name string) (io.ReadCloser, error) {
	return t.share.Open(t.full(name))
}

func (t *smbTarget) List(_ context.Context) ([]Object, error) {
	base := t.base
	if base == "" {
		base = "."
	}
	infos, err := t.share.ReadDir(base)
	if err != nil {
		return nil, err
	}
	var objs []Object
	for _, fi := range infos {
		if fi.IsDir() {
			continue
		}
		objs = append(objs, Object{Name: fi.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	return objs, nil
}

func (t *smbTarget) Delete(_ context.Context, name string) error {
	return t.share.Remove(t.full(name))
}

func (t *smbTarget) Close() error {
	if t.share != nil {
		t.share.Umount()
	}
	if t.session != nil {
		t.session.Logoff()
	}
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}
