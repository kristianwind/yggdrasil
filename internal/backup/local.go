package backup

import (
	"context"
	"io"
	"os"
	"path/filepath"
)

// localTarget stores archives in a filesystem directory. This also serves NFS
// and CIFS/SMB shares that are already mounted on the host (the common homelab
// setup): point Path at the mountpoint.
type localTarget struct {
	base string
}

func openLocal(cfg Config) (Target, error) {
	if err := os.MkdirAll(cfg.Path, 0755); err != nil {
		return nil, err
	}
	return &localTarget{base: cfg.Path}, nil
}

func (t *localTarget) Put(_ context.Context, name string, r io.Reader) (int64, error) {
	dest := filepath.Join(t.base, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return 0, err
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(f, r)
}

func (t *localTarget) Get(_ context.Context, name string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(t.base, name))
}

func (t *localTarget) List(_ context.Context) ([]Object, error) {
	entries, err := os.ReadDir(t.base)
	if err != nil {
		return nil, err
	}
	var objs []Object
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		objs = append(objs, Object{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return objs, nil
}

func (t *localTarget) Delete(_ context.Context, name string) error {
	return os.Remove(filepath.Join(t.base, name))
}

func (t *localTarget) Close() error { return nil }
