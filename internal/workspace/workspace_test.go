package workspace

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// Quoting is not argv-safety. `git clone` was once built as a shell string
// with both arguments shell-quoted, which is shell-safe and still wrong:
// quoting makes a value one argv element, and that element can still be an
// option. `git clone --upload-pack=<cmd>` executes <cmd>.
//
// These moved here with the code. They used to live in internal/cbx, which no
// longer owns repo resolution — internal/api needed the same thing, and a
// second copy is how the earlier design shipped a command injection in one
// package that the others did not have.

func TestRepoURLRejectsWhatGitWouldReadAsAnOption(t *testing.T) {
	for _, bad := range []string{
		"--upload-pack=touch /tmp/pwned",
		"-u/tmp/x",
		"--help",
		"owner/repo; touch /tmp/pwned",
		"owner repo",
		"not-a-repo",
		"a/b/c",
		"",
	} {
		if got, err := RepoURL(bad); err == nil {
			t.Errorf("RepoURL(%q) = %q, want an error", bad, got)
		}
	}
}

func TestRepoURLAcceptsShorthandAndFullURLs(t *testing.T) {
	for in, want := range map[string]string{
		"owner/repo":                        "https://github.com/owner/repo.git",
		"my-org/my.repo_v2":                 "https://github.com/my-org/my.repo_v2.git",
		"https://github.com/o/r.git":        "https://github.com/o/r.git",
		"git@github.com:o/r.git":            "git@github.com:o/r.git",
		"https://gitlab.com/group/proj.git": "https://gitlab.com/group/proj.git",
	} {
		got, err := RepoURL(in)
		if err != nil {
			t.Errorf("RepoURL(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("RepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrepareRejectsAHostileRepoAndLeavesNothingBehind(t *testing.T) {
	canary := filepath.Join(t.TempDir(), "pwned")
	dir := filepath.Join(t.TempDir(), "proj")

	if err := Prepare(dir, "--upload-pack=touch "+canary); err == nil {
		t.Fatal("Prepare accepted a repo value git would read as an option")
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatal("the payload executed")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a project directory was left behind after the failure")
	}
}

func TestPrepareCreatesAnEmptyDirectoryWithoutARepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if err := Prepare(dir, ""); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
}

func TestPrepareAcceptsAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := Prepare(dir, ""); err != nil {
		t.Errorf("Prepare on an existing directory: %v", err)
	}
}

func TestPrepareRefusesToCloneOverExistingWork(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prepare(dir, "owner/repo"); err == nil {
		t.Error("cloned into a non-empty directory — that would bury someone's work")
	}
}

// --- Root ---
//
// A box runs its sessions as the user it was provisioned with, so where
// project directories live has to be somewhere that user can write. This went
// untested while Root chose /workspace on existence alone, which looks fine to
// Stat and refuses every write afterwards.

func TestRootIsTheUsersWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got, want := Root(), filepath.Join(home, "workspace"); got != want {
		t.Errorf("Root() = %q, want %q", got, want)
	}
}

// Pins the decision, so nobody reintroduces the shared path as an
// optimisation: a directory at the filesystem root is one the session user
// does not own.
func TestRootIgnoresASharedWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := os.Stat("/workspace"); err == nil {
		t.Log("/workspace exists on this machine, which is the case worth checking")
	}
	if got := Root(); got == "/workspace" {
		t.Error("Root() chose /workspace")
	}
}

// There is no fallback. Everything else cbx owns lives in this home — the
// database, the config, the knowledge — so a home that cannot be written is a
// box that cannot run, and a second location would only move the failure
// somewhere harder to see.
func TestEnsureRefusesAWorkspaceItCannotWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through any mode")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	locked := filepath.Join(home, "workspace")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	got, err := Ensure()
	if err == nil {
		t.Fatalf("Ensure() returned %q for a directory it cannot write", got)
	}
	// The fix is to chown the directory or run as whoever owns it, so the
	// message has to name both.
	if !strings.Contains(err.Error(), locked) {
		t.Errorf("the error does not name the directory: %v", err)
	}
	if u, uerr := user.Current(); uerr == nil && !strings.Contains(err.Error(), u.Username) {
		t.Errorf("the error does not name the user: %v", err)
	}
}

// Existence is not the question: a directory owned by someone else looks fine
// to Stat and refuses every write afterwards.
func TestEnsureMakesTheWorkspaceWhenItIsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure(): %v", err)
	}
	if want := filepath.Join(home, "workspace"); got != want {
		t.Errorf("Ensure() = %q, want %q", got, want)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Errorf("the workspace was not created: %v", err)
	}
}

// Ensure reporting success on a path nothing can write is the failure this is
// all about, so both outcomes are checked by actually using it.
func TestRootReturnsSomethingPrepareCanUse(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through any mode")
	}
	cases := []struct {
		name string
		lock bool
	}{
		{"a usable workspace", false},
		{"one that cannot be written", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if c.lock {
				locked := filepath.Join(home, "workspace")
				if err := os.Mkdir(locked, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(locked, 0o755) })
			}
			root, err := Ensure()
			if c.lock {
				if err == nil {
					t.Fatalf("Ensure() returned %q for an unwritable workspace", root)
				}
				return
			}
			if err != nil {
				t.Fatalf("Ensure(): %v", err)
			}
			dir := filepath.Join(root, "a-project")
			if err := Prepare(dir, ""); err != nil {
				t.Fatalf("Prepare under %s: %v", root, err)
			}
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("the project directory is not there: %v", err)
			}
		})
	}
}
