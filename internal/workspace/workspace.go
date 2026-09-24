// Package workspace resolves where a session's files live.
//
// It is the only place that turns a session name and an optional repo into a
// directory on disk. That is deliberate: repo resolution was duplicated across
// three packages in an earlier design, and the copies drifted — one of them
// built `git clone` as a shell string and shipped a command injection the
// others did not have.
package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

// Root is where project directories live: ~/workspace.
//
// Never /workspace. A box runs its sessions as the user it was provisioned
// with, not as root, so a directory at the filesystem root is one they cannot
// write — and choosing it on existence, as this once did, failed at the
// session's first write rather than at startup.
//
// Pure: the path, with no claim that it works. Ensure is what checks.
func Root() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "workspace")
	}
	return filepath.Join(home, "workspace")
}

// Ensure returns the root, having made it usable, or explains why it is not.
//
// There is no fallback. Everything else cbx owns already lives in this home —
// the database, the config, the knowledge, each key's settings — so a home
// that cannot be written is a box that cannot run, and a second location would
// only move the failure somewhere harder to see.
//
// Written to rather than stat'd: a directory owned by someone else looks fine
// to Stat and refuses every write afterwards.
func Ensure() (string, error) {
	dir := Root()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("%s cannot be created: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".cbx-probe-*")
	if err != nil {
		return "", fmt.Errorf("%s is not writable by %s: %w", dir, currentUser(), err)
	}
	probe.Close()
	os.Remove(probe.Name())
	return dir, nil
}

// currentUser names who the failing process is, since the fix is usually to
// chown the directory to them or to run as whoever owns it.
func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return fmt.Sprintf("uid %d", os.Geteuid())
}

// Prepare makes a project directory ready: cloned, existing, or new.
func Prepare(dir, repo string) error {
	if _, err := os.Stat(dir); err == nil {
		if repo != "" {
			// Refuse rather than clone over someone's work.
			if entries, _ := os.ReadDir(dir); len(entries) > 0 {
				return fmt.Errorf("%s already exists and is not empty — remove it or omit the repo", dir)
			}
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if repo == "" {
		return nil
	}
	url, err := RepoURL(repo)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	// exec.Command, not a shell string. Shell-quoting makes a value safe for
	// the shell but not for argv: git reads a leading dash as an option, and
	// `git clone --upload-pack=...` runs an arbitrary command. "--" ends
	// option parsing, and going through exec directly means there is no shell
	// to quote for in the first place.
	out, err := exec.Command("git", "clone", "--", url, dir).CombinedOutput()
	if err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("clone %s: %w: %s", repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RepoURL expands owner/repo shorthand and rejects anything git would read as
// an option rather than a repository.
func RepoURL(repo string) (string, error) {
	if strings.HasPrefix(repo, "-") {
		return "", fmt.Errorf("invalid repo %q: leading dash would be read as a git option", repo)
	}
	if strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@") {
		return repo, nil
	}
	// Shorthand must look like owner/repo and nothing else.
	if !shorthand.MatchString(repo) {
		return "", fmt.Errorf("invalid repo %q: expected owner/repo or a full git URL", repo)
	}
	return "https://github.com/" + repo + ".git", nil
}

var shorthand = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
