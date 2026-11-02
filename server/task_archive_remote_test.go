package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/archivestore"
	pgdb "github.com/Autumn-27/artex/db"
)

func TestRemoteArchiveLifecycle(t *testing.T) {
	if os.Getenv("ARTEX_PG_DSN") == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	t.Setenv("ARTEX_ARCHIVE_STORAGE", "local")
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	s.archiveWG.Wait()
	s.ctx = context.Background()
	var mu sync.Mutex
	objects := map[string][]byte{}
	corrupt, failDelete := false, false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			w.Header().Set("ETag", `"test"`)
		case "GET":
			if corrupt {
				_, _ = w.Write([]byte("corrupt"))
			} else {
				_, _ = w.Write(objects[r.URL.Path])
			}
		case "DELETE":
			if failDelete {
				w.WriteHeader(403)
				return
			}
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(400)
		}
	}))
	defer remote.Close()
	s.archiveStore, err = archivestore.New(archivestore.Config{Endpoint: remote.URL, Bucket: "test", Prefix: "tasks", AccessKey: "test", SecretKey: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("spaces round trip", "preserve data", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	defer func() { _, _ = m.pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, id); _ = m.pg.DeleteTask(id) }()
	if err := m.pg.SetPaused(id, true); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(m.dir, "tasks", task.ID, "workspace", "important.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("keep this content"), 0600); err != nil {
		t.Fatal(err)
	}
	pgTask, err := m.pg.GetTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exploration(pgTask.ExplorationID).AppendActivity(pgdb.Activity{Worker: "system", Kind: "system", Summary: "archive proof"}); err != nil {
		t.Fatal(err)
	}
	job, err := m.pg.QueueTaskArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	// A corrupt readback must roll back filesystem staging and leave PG intact.
	mu.Lock()
	corrupt = true
	mu.Unlock()
	if err := s.runOneTaskArchiveJob(); err == nil {
		t.Fatal("expected integrity failure")
	}
	if got, _ := os.ReadFile(file); string(got) != "keep this content" {
		t.Fatal("upload failure lost source")
	}
	if task, err := m.pg.GetTask(id); err != nil || task == nil {
		t.Fatal("upload failure lost task")
	}
	mu.Lock()
	corrupt = false
	mu.Unlock()
	if err := s.recoverRemoteArchiveJournals(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.QueueTaskArchive(id); err != nil {
		t.Fatal(err)
	}
	if err := s.runOneTaskArchiveJob(); err != nil {
		t.Fatal(err)
	}
	job, err = m.pg.GetTaskArchive(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != pgdb.ArchiveReady || job.Storage != "s3" || !strings.HasPrefix(job.ArchivePath, "s3://") {
		t.Fatalf("unexpected archive %+v", job)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("workspace not released")
	}
	if _, err := os.Stat(taskArchivePath(m.dir, job.ID, task.ID)); !os.IsNotExist(err) {
		t.Fatal("local package retained")
	}
	// Simulate the crash window after commit: cleanup keeps the remote object.
	journal, err := s.writeRemoteArchiveJournal(remoteArchiveJournal{ArchiveID: job.ID, URI: job.ArchivePath, LocalPath: taskArchivePath(m.dir, job.ID, task.ID), Operation: "upload"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.recoverRemoteArchiveJournal(journal); err != nil {
		t.Fatal(err)
	}
	// Restoration commits files and PG first. Failed remote deletion is retryable.
	mu.Lock()
	failDelete = true
	mu.Unlock()
	if _, err := m.pg.QueueTaskArchiveRestore(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.runOneTaskArchiveJob(); err == nil {
		t.Fatal("expected remote delete failure")
	}
	if got, _ := os.ReadFile(file); string(got) != "keep this content" {
		t.Fatal("restored content differs")
	}
	if restored, err := m.pg.IsTaskArchiveRestored(job.ID); err != nil || !restored {
		t.Fatalf("PG restore %v %v", restored, err)
	}
	mu.Lock()
	failDelete = false
	mu.Unlock()
	if _, err := m.pg.QueueTaskArchiveRestore(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.runOneTaskArchiveJob(); err != nil {
		t.Fatal(err)
	}
	if item, err := m.pg.GetTaskArchive(job.ID); err != nil || item != nil {
		t.Fatal("restore did not consume stub")
	}
	var count int
	if err := m.pg.QueryRow(`SELECT count(*) FROM activity WHERE exploration_id=$1 AND summary='archive proof'`, pgTask.ExplorationID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("activity restore count=%d err=%v", count, err)
	}
	// Re-archive, then crash/retry the deletion outbox after PG removed its stub.
	job, err = m.pg.QueueTaskArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runOneTaskArchiveJob(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	failDelete = true
	mu.Unlock()
	if _, err := m.pg.QueueTaskArchiveDelete(job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.runOneTaskArchiveJob(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	failDelete = false
	mu.Unlock()
	if err := s.recoverRemoteArchiveJournals(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	remaining := len(objects)
	mu.Unlock()
	if remaining != 0 {
		t.Fatalf("left %d objects", remaining)
	}
}
