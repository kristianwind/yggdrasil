package backup

import (
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sftpTarget struct {
	ssh  *ssh.Client
	sftp *sftp.Client
	base string
}

func openSFTP(cfg Config) (Target, error) {
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	sshCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // homelab; key pinning is a later enhancement
		Timeout:         10 * time.Second,
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(port))
	conn, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("sftp ssh dial: %w", err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("sftp client: %w", err)
	}
	base := cfg.Path
	if base == "" {
		base = "."
	}
	client.MkdirAll(base)
	return &sftpTarget{ssh: conn, sftp: client, base: base}, nil
}

func (t *sftpTarget) Put(_ context.Context, name string, r io.Reader) (int64, error) {
	dest := path.Join(t.base, name)
	t.sftp.MkdirAll(path.Dir(dest))
	f, err := t.sftp.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.ReadFrom(r)
}

func (t *sftpTarget) Get(_ context.Context, name string) (io.ReadCloser, error) {
	return t.sftp.Open(path.Join(t.base, name))
}

func (t *sftpTarget) List(_ context.Context) ([]Object, error) {
	infos, err := t.sftp.ReadDir(t.base)
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

func (t *sftpTarget) Delete(_ context.Context, name string) error {
	return t.sftp.Remove(path.Join(t.base, name))
}

func (t *sftpTarget) Close() error {
	if t.sftp != nil {
		t.sftp.Close()
	}
	if t.ssh != nil {
		return t.ssh.Close()
	}
	return nil
}
