package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Config is the object-storage half of the process configuration; the caller
// fills it from config.Config so this package stays independent of it.
type Config struct {
	Endpoint string // full URL, e.g. http://localhost:9000
	Bucket   string
	Key      string
	Secret   string
}

// Client stores and serves objects in one bucket.
type Client struct {
	api    *minio.Client
	bucket string
}

var ErrNotConfigured = errors.New("blob: endpoint, bucket, key, and secret are all required")

// New builds a client for cfg. The endpoint's scheme decides whether the
// connection is TLS, so the same code reaches MinIO and a hosted S3.
func New(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.Key == "" || cfg.Secret == "" {
		return nil, ErrNotConfigured
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("blob: bad endpoint %q", cfg.Endpoint)
	}
	api, err := minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.Key, cfg.Secret, ""),
		Secure: u.Scheme == "https",
	})
	if err != nil {
		return nil, fmt.Errorf("blob: client: %w", err)
	}
	return &Client{api: api, bucket: cfg.Bucket}, nil
}

// EnsureBucket creates the bucket when it does not exist yet, so a fresh
// MinIO volume needs no manual setup.
func (c *Client) EnsureBucket(ctx context.Context) error {
	exists, err := c.api.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("blob: check bucket %s: %w", c.bucket, err)
	}
	if exists {
		return nil
	}
	if err := c.api.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{}); err != nil {
		// A racing process may have created it between the two calls.
		if exists, checkErr := c.api.BucketExists(ctx, c.bucket); checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("blob: create bucket %s: %w", c.bucket, err)
	}
	return nil
}

// Put writes size bytes from r at key. size must be exact; pass -1 only when
// it is genuinely unknown, since that buffers the whole object.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.api.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("blob: put %s: %w", key, err)
	}
	return nil
}

// Get opens the object at key. The caller closes the reader.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.api.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("blob: get %s: %w", key, err)
	}
	// GetObject is lazy: a missing key only surfaces on the first read, so
	// stat here rather than handing back a reader that fails later.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, fmt.Errorf("blob: get %s: %w", key, err)
	}
	return obj, nil
}

// SignedGetURL returns a URL that downloads key without credentials for ttl,
// presented to the browser as filename.
func (c *Client) SignedGetURL(ctx context.Context, key, filename string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("blob: signed URL for %s needs a positive lifetime", key)
	}
	params := url.Values{}
	if filename != "" {
		params.Set("response-content-disposition", "attachment; filename="+quoteFilename(filename))
	}
	u, err := c.api.PresignedGetObject(ctx, c.bucket, key, ttl, params)
	if err != nil {
		return "", fmt.Errorf("blob: sign %s: %w", key, err)
	}
	return u.String(), nil
}

// Delete removes key; a key that is already gone is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := c.api.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("blob: delete %s: %w", key, err)
	}
	return nil
}

// quoteFilename renders name as a quoted-string header parameter, dropping
// the characters that would let it break out of the quotes.
func quoteFilename(name string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, name)
	if clean == "" {
		clean = "download"
	}
	return `"` + clean + `"`
}
