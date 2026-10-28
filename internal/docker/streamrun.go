package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// StreamOptions describes a one-shot container that is fed and read as streams,
// for a tool such as rclone that uploads what it reads on stdin and downloads to
// stdout.
type StreamOptions struct {
	Image string
	Argv  []string
	Env   []string // never secrets: they stay readable in `docker inspect`

	// Files are written into the container before it starts (path → content,
	// mode 0600). This is how a credential reaches it: unlike an environment
	// variable it does not appear in `docker inspect`, and unlike a bind mount it
	// needs no directory the daemon can see.
	Files map[string][]byte
	// Collect lists files read back out after the container exits — a tool that
	// refreshes its own login token writes the new one into its config file, and
	// it is lost with the container unless somebody keeps it.
	Collect []string

	Stdin  io.Reader // nil: no stdin
	Stdout io.Writer // nil: discarded
	Stderr io.Writer // nil: discarded
}

// RunStream runs the container to completion and returns the collected files.
// A non-zero exit is an error; the files are returned with it, because a tool
// can have refreshed its token and still failed afterwards.
func (c *Client) RunStream(ctx context.Context, opts StreamOptions) (map[string][]byte, error) {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	resp, err := c.dc.ContainerCreate(ctx, &container.Config{
		Image:        opts.Image,
		Env:          opts.Env,
		Entrypoint:   opts.Argv,
		OpenStdin:    opts.Stdin != nil,
		StdinOnce:    opts.Stdin != nil,
		AttachStdin:  opts.Stdin != nil,
		AttachStdout: true,
		AttachStderr: true,
	}, &container.HostConfig{
		Resources: container.Resources{PidsLimit: defaultPidsLimit()},
	}, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	cid := resp.ID
	defer c.dc.ContainerRemove(context.Background(), cid, container.RemoveOptions{Force: true, RemoveVolumes: true}) //nolint:errcheck

	if len(opts.Files) > 0 {
		var buf bytes.Buffer
		if err := writeTar(&buf, opts.Files); err != nil {
			return nil, err
		}
		if err := c.dc.CopyToContainer(ctx, cid, "/", &buf, container.CopyToContainerOptions{}); err != nil {
			return nil, fmt.Errorf("copy files into container: %w", err)
		}
	}

	// Attach before start, or the first bytes of output can be missed.
	hj, err := c.dc.ContainerAttach(ctx, cid, container.AttachOptions{
		Stream: true, Stdin: opts.Stdin != nil, Stdout: true, Stderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	defer hj.Close()

	if err := c.dc.ContainerStart(ctx, cid, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	if opts.Stdin != nil {
		go func() {
			io.Copy(hj.Conn, opts.Stdin) //nolint:errcheck
			hj.CloseWrite()              //nolint:errcheck // EOF on the container's stdin
		}()
	}
	_, copyErr := stdcopy.StdCopy(opts.Stdout, opts.Stderr, hj.Reader)
	if copyErr != nil {
		// The reader of the output went away (a restore cancelled halfway).
		// Stop the container rather than wait for it to notice.
		kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c.dc.ContainerKill(kctx, cid, "KILL") //nolint:errcheck
		cancel()
	}

	statusCh, errCh := c.dc.ContainerWait(ctx, cid, container.WaitConditionNotRunning)
	var exitCode int64
	select {
	case err := <-errCh:
		if err != nil {
			return nil, err
		}
	case st := <-statusCh:
		exitCode = st.StatusCode
	}

	files := map[string][]byte{}
	for _, p := range opts.Collect {
		if b, err := c.readFile(ctx, cid, p); err == nil {
			files[p] = b
		}
	}
	if copyErr != nil {
		return files, fmt.Errorf("output stream: %w", copyErr)
	}
	if exitCode != 0 {
		return files, fmt.Errorf("%s exited with code %d", path.Base(opts.Argv[0]), exitCode)
	}
	return files, nil
}

func writeTar(w io.Writer, files map[string][]byte) error {
	tw := tar.NewWriter(w)
	dirs := map[string]bool{}
	for p, content := range files {
		p = strings.TrimPrefix(path.Clean(p), "/")
		// Parent directories first, owned by root and private.
		parts := strings.Split(path.Dir(p), "/")
		for i := range parts {
			d := strings.Join(parts[:i+1], "/")
			if d == "." || dirs[d] {
				continue
			}
			dirs[d] = true
			if err := tw.WriteHeader(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: p, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(content))}); err != nil {
			return err
		}
		if _, err := tw.Write(content); err != nil {
			return err
		}
	}
	return tw.Close()
}

// readFile copies one regular file out of a (stopped) container.
func (c *Client) readFile(ctx context.Context, cid, p string) ([]byte, error) {
	rc, _, err := c.dc.CopyFromContainer(ctx, cid, p)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 1<<20))
		}
	}
}
