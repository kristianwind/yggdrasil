package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Amazon S3 and everything that speaks its API: Cloudflare R2, Backblaze B2,
// Wasabi, DigitalOcean Spaces, Hetzner, Scaleway, OVH, MinIO, Garage, Ceph.
//
// Google Cloud Storage has an S3 endpoint too, but it is not used for it: the
// client cannot stream an archive of unknown size to it (no multipart upload
// through that endpoint), so a backup would have to be spooled to disk first.
// GCS has its own target (gcs.go).

// s3PartSize is the size of each multipart chunk. An archive is streamed, so
// its size is unknown when the upload starts, and S3 allows 10 000 parts: 64 MiB
// parts cap one archive at 640 GB, and one part is what the upload holds in
// memory.
const s3PartSize = 64 << 20

type s3Target struct {
	c      *minio.Client
	bucket string
	prefix string
}

// splitEndpoint turns "https://host[:port]", "http://host:port" or a bare host
// into what the client wants. A bare host means https.
func splitEndpoint(raw string) (host string, secure bool, err error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false, fmt.Errorf("endpoint must be a host such as s3.eu-central-1.amazonaws.com")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", false, fmt.Errorf("endpoint must use https (or http for a server on your own network)")
	}
	if strings.Trim(u.Path, "/") != "" {
		return "", false, fmt.Errorf("endpoint must be a host without a path — put the bucket in Bucket and any folder in Path")
	}
	return u.Host, u.Scheme == "https", nil
}

func openS3(cfg Config) (Target, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3: bucket is required")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("s3: access key ID and secret access key are required")
	}
	endpoint := cfg.Host
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	host, secure, err := splitEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.Username, cfg.Password, ""),
		Secure: secure,
		Region: cfg.Region,
	}
	// Path-style addressing (host/bucket/key) works on every S3 implementation;
	// virtual-host style (bucket.host/key) only where DNS has a name per bucket,
	// which a MinIO on the LAN does not. AWS itself wants virtual-host style and
	// the client picks it there on its own.
	if !strings.HasSuffix(host, "amazonaws.com") {
		opts.BucketLookup = minio.BucketLookupPath
	}
	c, err := minio.New(host, opts)
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	return &s3Target{c: c, bucket: cfg.Bucket, prefix: cleanPrefix(cfg.Path)}, nil
}

// cleanPrefix turns a folder inside a bucket into an object-key prefix: no
// leading slash, one trailing slash, "" for the bucket root.
func cleanPrefix(p string) string {
	p = strings.Trim(path.Clean("/"+strings.TrimSpace(p)), "/")
	if p == "" || p == "." {
		return ""
	}
	return p + "/"
}

func (t *s3Target) key(name string) string { return t.prefix + strings.TrimLeft(name, "/") }

func (t *s3Target) Put(ctx context.Context, name string, r io.Reader) (int64, error) {
	info, err := t.c.PutObject(ctx, t.bucket, t.key(name), r, -1, minio.PutObjectOptions{
		ContentType: "application/gzip",
		PartSize:    s3PartSize,
	})
	if err != nil {
		return 0, s3Err(err)
	}
	return info.Size, nil
}

func (t *s3Target) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	obj, err := t.c.GetObject(ctx, t.bucket, t.key(name), minio.GetObjectOptions{})
	if err != nil {
		return nil, s3Err(err)
	}
	// GetObject is lazy: a missing object or a refused key only shows on the
	// first read. Stat now, so a restore fails here with the reason.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, s3Err(err)
	}
	return obj, nil
}

func (t *s3Target) List(ctx context.Context) ([]Object, error) {
	var objs []Object
	for o := range t.c.ListObjects(ctx, t.bucket, minio.ListObjectsOptions{Prefix: t.prefix}) {
		if o.Err != nil {
			return nil, s3Err(o.Err)
		}
		if strings.HasSuffix(o.Key, "/") {
			continue // a folder
		}
		objs = append(objs, Object{Name: strings.TrimPrefix(o.Key, t.prefix), Size: o.Size, ModTime: o.LastModified})
	}
	return objs, nil
}

func (t *s3Target) Delete(ctx context.Context, name string) error {
	return s3Err(t.c.RemoveObject(ctx, t.bucket, t.key(name), minio.RemoveObjectOptions{}))
}

func (t *s3Target) Close() error { return nil }

// s3Err adds what to do about the errors an admin can fix.
func s3Err(err error) error {
	if err == nil {
		return nil
	}
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchBucket":
		return fmt.Errorf("%w — create the bucket first; the panel does not create it", err)
	case "InvalidAccessKeyId", "SignatureDoesNotMatch":
		return fmt.Errorf("%w — check the access key ID and secret", err)
	case "AccessDenied":
		return fmt.Errorf("%w — the key needs to list, read, write and delete objects in this bucket", err)
	case "AuthorizationHeaderMalformed", "PermanentRedirect":
		return fmt.Errorf("%w — check the region", err)
	}
	return err
}
