// Package archivestore stores immutable cold-task packages in S3-compatible storage.
package archivestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Config struct {
	Endpoint, Region, Bucket, Prefix, AccessKey, SecretKey string
	PathStyle                                              bool
}

type Store struct {
	client         *s3.Client
	bucket, prefix string
}

// FromEnv returns nil for the default local backend. A configured but invalid
// backend is an error, never a silent fallback to the machine's local disk.
func FromEnv() (*Store, error) {
	switch os.Getenv("ARTEX_ARCHIVE_STORAGE") {
	case "", "local":
		return nil, nil
	case "s3":
		return New(Config{
			Endpoint: os.Getenv("ARTEX_ARCHIVE_S3_ENDPOINT"), Region: os.Getenv("ARTEX_ARCHIVE_S3_REGION"),
			Bucket: os.Getenv("ARTEX_ARCHIVE_S3_BUCKET"), Prefix: os.Getenv("ARTEX_ARCHIVE_S3_PREFIX"),
			AccessKey: os.Getenv("ARTEX_ARCHIVE_S3_ACCESS_KEY_ID"), SecretKey: os.Getenv("ARTEX_ARCHIVE_S3_SECRET_ACCESS_KEY"),
			PathStyle: os.Getenv("ARTEX_ARCHIVE_S3_PATH_STYLE") == "true",
		})
	default:
		return nil, errors.New("ARTEX_ARCHIVE_STORAGE must be local or s3")
	}
}

func New(c Config) (*Store, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, errors.New("archive S3 endpoint must be an HTTPS origin (HTTP allowed only on loopback)")
	}
	c.Prefix = strings.Trim(c.Prefix, "/")
	if c.Prefix == "" {
		c.Prefix = "artex/tasks"
	}
	if path.Clean(c.Prefix) != c.Prefix || strings.Contains(c.Prefix, "..") || strings.ContainsAny(c.Prefix, "\\?#") {
		return nil, errors.New("invalid archive S3 prefix")
	}
	if c.Bucket == "" || strings.ContainsAny(c.Bucket, "/\\:@?# ") || c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("archive S3 bucket and dedicated access credentials are required")
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	client := s3.New(s3.Options{
		Region: c.Region, BaseEndpoint: aws.String(c.Endpoint), UsePathStyle: c.PathStyle,
		Credentials:                credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, ""),
		HTTPClient:                 &http.Client{Transport: transport},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client, bucket: c.Bucket, prefix: c.Prefix + "/"}, nil
}

func (s *Store) URI(taskID, archiveID int64, checksum string) string {
	return "s3://" + s.bucket + "/" + s.prefix + fmt.Sprintf("task-%d-%d-%s.tar.zst", taskID, archiveID, checksum)
}

func (s *Store) key(uri string) (string, error) {
	if s == nil {
		return "", errors.New("S3 archive storage is not configured")
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "s3" || u.Host != s.bucket || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("archive object is outside the configured bucket")
	}
	key := strings.TrimPrefix(u.Path, "/")
	if !strings.HasPrefix(key, s.prefix) || len(key) <= len(s.prefix) || path.Clean(key) != key || strings.Contains(key, "\\") {
		return "", errors.New("archive object is outside the configured prefix")
	}
	return key, nil
}

func (s *Store) Validate(uri string) error { _, err := s.key(uri); return err }

// Upload verifies the entire object by reading it back, including multipart
// uploads whose ETag is not a checksum. Callers must retain source data until
// this succeeds AND the database has committed the remote location.
func (s *Store) Upload(ctx context.Context, uri, filename, checksum string) error {
	key, err := s.key(uri)
	if err != nil {
		return err
	}
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	uploader := manager.NewUploader(s.client, func(u *manager.Uploader) {
		u.PartSize = 8 << 20
		u.Concurrency = 2
		u.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: f,
		ContentType: aws.String("application/zstd"), Metadata: map[string]string{"sha256": checksum},
	})
	if err != nil {
		return fmt.Errorf("upload archive: %w", err)
	}
	return s.readVerified(ctx, key, io.Discard, checksum, info.Size())
}

func (s *Store) readVerified(ctx context.Context, key string, out io.Writer, checksum string, expectedSize int64) error {
	if decoded, err := hex.DecodeString(checksum); err != nil || len(decoded) != sha256.Size {
		return errors.New("invalid archive SHA-256")
	}
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	defer object.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(object.Body, expectedSize+1))
	if err != nil {
		return err
	}
	if n != expectedSize || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), checksum) {
		return errors.New("remote archive size or SHA-256 mismatch; source data retained")
	}
	return nil
}

func (s *Store) Download(ctx context.Context, uri, filename, checksum string, size int64) (err error) {
	key, err := s.key(uri)
	if err != nil {
		return err
	}
	if size <= 0 {
		return errors.New("invalid archive size")
	}
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			_ = os.Remove(filename)
		}
	}()
	if err = s.readVerified(ctx, key, f, checksum, size); err != nil {
		return err
	}
	return f.Sync()
}

func (s *Store) Delete(ctx context.Context, uri string) error {
	key, err := s.key(uri)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}
