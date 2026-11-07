package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pgdb "github.com/Autumn-27/artex/db"
)

func TestWorkspaceLinksArchiveRestore(t *testing.T) {
	data := t.TempDir()
	source := filepath.Join(data, "tasks", "43")
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "tool"), []byte("tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(data, "private")
	if err := os.WriteFile(external, []byte("outside contents"), 0o600); err != nil {
		t.Fatal(err)
	}

	internal := map[string]string{
		"dir": "bin", "through-dir": "dir/tool", "bin/back": "../bin/tool",
		"abs": filepath.Join(source, "bin", "tool"), "dangling": "future-file",
		"bin/up": "..",
	}
	skipped := map[string]string{
		"outside-absolute": external, "outside-relative": "../../private",
		"via-outside": "outside-relative", "via-dotdot": "bin/up/../private",
	}
	for _, group := range []map[string]string{internal, skipped} {
		for name, target := range group {
			if err := os.Symlink(target, filepath.Join(source, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	stage, err := stageTaskArchiveFiles(data, 17, "43", 43)
	if err != nil {
		t.Fatal(err)
	}
	pkg := taskArchivePath(data, 17, "43")
	snapshot := &pgdb.TaskArchiveSnapshot{TaskID: 43, ExplorationID: 43, FormatVersion: pgdb.TaskArchiveFormatVersion}
	_, _, sum, err := writeTaskArchivePackage(pkg, stage.payload, source, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := stage.commit(); err != nil {
		t.Fatal(err)
	}

	restoredData := t.TempDir() // absolute internal links must survive relocation.
	extracted := filepath.Join(restoredData, "extracted")
	if err := extractTaskArchivePackage(pkg, sum, extracted); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(extracted, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded pgdb.TaskArchiveSnapshot
	if err := json.Unmarshal(manifest, &decoded); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(extracted, "files", "tasks", "43")
	// No links or copies of their targets are materialized by tar extraction.
	for name := range skipped {
		if _, err := os.Lstat(filepath.Join(staged, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected entry %q: %v", name, err)
		}
	}
	warnings, err := restoreWorkspaceLinks(staged, &decoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != len(skipped) {
		t.Fatalf("warnings: %v", warnings)
	}
	installed, err := installTaskArchiveFiles(restoredData, extracted, "43", 17)
	if err != nil {
		t.Fatal(err)
	}
	if err := installed.commit(); err != nil {
		t.Fatal(err)
	}
	hot := filepath.Join(restoredData, "tasks", "43")
	for name, target := range skipped {
		if decoded.WorkspaceLinks.Targets[name] != target || !strings.Contains(strings.Join(warnings, "\n"), `"`+name+`" -> "`+target+`"`) {
			t.Fatalf("%s -> %s missing from manifest or warnings: %v", name, target, warnings)
		}
		if _, err := os.Lstat(filepath.Join(hot, name)); !os.IsNotExist(err) {
			t.Fatalf("external link restored %q: %v", name, err)
		}
	}
	for name, target := range internal {
		got, err := os.Readlink(filepath.Join(hot, name))
		if err != nil || (name != "abs" && got != target) || (name == "abs" && filepath.IsAbs(got)) {
			t.Fatalf("restored link %s=%q: %v", name, got, err)
		}
	}
	for _, name := range []string{"through-dir", "bin/back", "abs"} {
		got, err := os.ReadFile(filepath.Join(hot, name))
		if err != nil || string(got) != "tool" {
			t.Fatalf("read link %s: %q %v", name, got, err)
		}
	}
}

func TestWorkspaceLinksRejectMalformedManifest(t *testing.T) {
	for _, name := range []string{"../escape", "/escape", ".", "alias/file", "real-file", "dir/../escape"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "real-file"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			s := &pgdb.TaskArchiveSnapshot{FormatVersion: 4, WorkspaceLinks: &pgdb.TaskArchiveWorkspaceLinks{
				Targets: map[string]string{"alias": ".", name: "target"},
			}}
			if _, err := restoreWorkspaceLinks(workspace, s); err == nil {
				t.Fatal("accepted malformed link metadata")
			}
			if _, err := os.Lstat(filepath.Join(workspace, "alias")); !os.IsNotExist(err) {
				t.Fatal("created links before validating all locations")
			}
		})
	}
	for _, v := range []int{1, 2, 3} {
		s := &pgdb.TaskArchiveSnapshot{FormatVersion: v}
		if _, err := restoreWorkspaceLinks("", s); err != nil {
			t.Fatal(err)
		}
		s.WorkspaceLinks = &pgdb.TaskArchiveWorkspaceLinks{}
		if _, err := restoreWorkspaceLinks("", s); err == nil {
			t.Fatalf("accepted link metadata in v%d", v)
		}
	}
}
