package archivestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestS3RoundTripAndIntegrity(t *testing.T) {
	for _, size := range []int{1024, 10 << 20} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			var mu sync.Mutex
			var object []byte
			parts := map[int][]byte{}
			corrupt, multipart := false, false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				q := r.URL.Query()
				w.Header().Set("ETag", `"test-etag"`)
				switch {
				case r.Method == "POST" && q.Has("uploads"):
					multipart = true
					fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>test-upload</UploadId></InitiateMultipartUploadResult>`)
				case r.Method == "PUT" && q.Has("partNumber"):
					n, _ := strconv.Atoi(q.Get("partNumber"))
					parts[n], _ = io.ReadAll(r.Body)
				case r.Method == "POST" && q.Has("uploadId"):
					for i := 1; i <= len(parts); i++ {
						object = append(object, parts[i]...)
					}
					fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>test-etag</ETag></CompleteMultipartUploadResult>`)
				case r.Method == "PUT":
					object, _ = io.ReadAll(r.Body)
				case r.Method == "GET":
					if corrupt {
						_, _ = w.Write([]byte("damaged"))
					} else {
						_, _ = w.Write(object)
					}
				case r.Method == "DELETE":
					object = nil
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(400)
				}
			}))
			defer srv.Close()
			store, err := New(Config{Endpoint: srv.URL, Bucket: "test-bucket", Prefix: "test/tasks", AccessKey: "test", SecretKey: "test", PathStyle: true})
			if err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte{0x42}, size)
			h := sha256.Sum256(data)
			checksum := hex.EncodeToString(h[:])
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			uri := store.URI(1, 2, checksum)
			ctx := context.Background()
			if err := store.Upload(ctx, uri, source, checksum); err != nil {
				t.Fatal(err)
			}
			if multipart != (size > 8<<20) {
				t.Fatalf("multipart=%v size=%d", multipart, size)
			}
			dest := filepath.Join(root, "download")
			if err := store.Download(ctx, uri, dest, checksum, int64(size)); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(dest)
			if !bytes.Equal(got, data) {
				t.Fatal("download differs")
			}
			mu.Lock()
			corrupt = true
			mu.Unlock()
			bad := filepath.Join(root, "bad")
			if err := store.Download(ctx, uri, bad, checksum, int64(size)); err == nil {
				t.Fatal("accepted corrupt object")
			}
			if _, err := os.Stat(bad); !os.IsNotExist(err) {
				t.Fatal("left corrupt download")
			}
			if err := store.Upload(ctx, uri, source, checksum); err == nil {
				t.Fatal("upload accepted failed readback")
			}
			if err := store.Delete(ctx, uri); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestS3RejectsForeignLocations(t *testing.T) {
	s, err := New(Config{Endpoint: "https://sfo3.digitaloceanspaces.com", Bucket: "allowed", Prefix: "archives/tasks", AccessKey: "test", SecretKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{"s3://other/archives/tasks/a", "s3://allowed/archives/other/a", "s3://allowed/archives/tasks/../a", "s3://allowed/archives/tasks/%2e%2e/a", "s3://allowed/archives/tasks/a?secret=x", "s3://user@allowed/archives/tasks/a", "file:///etc/passwd"} {
		if err := s.Validate(uri); err == nil {
			t.Errorf("accepted %s", uri)
		}
	}
	var absent *Store
	if absent.Validate("s3://allowed/archives/tasks/a") == nil {
		t.Fatal("missing config accepted")
	}
}
