package boxconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The allowlist is the only gate the API has. Claude Code reports is_error: false
// for a command that does not exist and for one that refuses to run without a
// terminal, so nothing downstream can tell a refusal from a success — these
// tests are what stand between a caller and a silent no-op.

func TestTheShippedDefaultIsValid(t *testing.T) {
	s, err := Parse(Default())
	if err != nil {
		t.Fatalf("the shipped config does not parse: %v", err)
	}
	if len(s.Commands) == 0 {
		t.Fatal("the shipped config declares no commands")
	}
}

func TestClearIsNeverForwarded(t *testing.T) {
	s, err := Parse(Default())
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Resolve("/clear")
	if err != nil {
		t.Fatalf("/clear is not allowed by the shipped config: %v", err)
	}
	// Forwarded, /clear reports success, forks a new session id and leaves the
	// transcript intact. Measured, not assumed.
	if c.Effect != RotateSession {
		t.Errorf("/clear effect = %q, want %q — forwarding it does not clear anything", c.Effect, RotateSession)
	}
}

func TestACommandNotInTheSpecIsRefused(t *testing.T) {
	s := &Config{Commands: []Command{{Name: "/model", Effect: Forward}}}
	if _, err := s.Resolve("/definitely-not-declared"); err == nil {
		t.Fatal("an undeclared command was allowed — deny by default is the whole point")
	}
}

func TestRequiresArgsIsEnforced(t *testing.T) {
	s := &Config{Commands: []Command{{Name: "/model", Effect: Forward, RequiresArgs: true}}}
	if _, err := s.Resolve("/model"); err == nil {
		t.Error("bare /model was allowed — it only prints its usage and changes nothing")
	}
	if _, err := s.Resolve("/model sonnet"); err != nil {
		t.Errorf("/model sonnet was refused: %v", err)
	}
}

func TestResolveIgnoresSurroundingWhitespace(t *testing.T) {
	s := &Config{Commands: []Command{{Name: "/compact", Effect: Forward}}}
	if _, err := s.Resolve("  /compact  "); err != nil {
		t.Errorf("padded command was refused: %v", err)
	}
}

func TestAnEmptyCommandIsRefused(t *testing.T) {
	s := &Config{Commands: []Command{{Name: "/compact", Effect: Forward}}}
	if _, err := s.Resolve("   "); err == nil {
		t.Error("an empty command was accepted")
	}
}

func TestAMalformedSpecIsAnErrorNotAnEmptyAllowlist(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"not yaml", "commands: [oh dear: ["},
		{"unknown effect", "commands:\n  - name: /x\n    effect: teleport\n"},
		{"no name", "commands:\n  - effect: forward\n"},
		{"name without a slash", "commands:\n  - name: model\n    effect: forward\n"},
		{"declared twice", "commands:\n  - name: /x\n    effect: forward\n  - name: /x\n    effect: forward\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse([]byte(c.yaml)); err == nil {
				t.Error("parsed without error — a bad config must not degrade into an empty allowlist")
			}
		})
	}
}

func TestLoadFallsBackToTheDefaultWhenAbsent(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nothing-here.yaml"))
	if err != nil {
		t.Fatalf("Load on a missing file: %v", err)
	}
	if _, err := s.Resolve("/clear"); err != nil {
		t.Errorf("the fallback spec does not allow /clear: %v", err)
	}
}

func TestLoadRefusesAMalformedFileRatherThanFallingBack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cbx.yaml")
	if err := os.WriteFile(p, []byte("commands: [broken: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("a malformed config silently became the default — the operator's policy was replaced without saying so")
	}
}

func TestRefusalNamesTheFileToEdit(t *testing.T) {
	s := &Config{}
	_, err := s.Resolve("/whatever")
	if err == nil || !strings.Contains(err.Error(), "cbx.yaml") {
		t.Errorf("error %v does not say where to allow the command", err)
	}
}

// Absent and empty mean different things, and the difference is the safe one.
func TestARoleThatSaysNothingAboutDenyGetsTheDefaults(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nroles:\n  unfinished: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.Role("unfinished")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Deny) == 0 {
		t.Fatal("an unfinished role came out unrestricted")
	}
	// The box's own guide is what these protect.
	var guards bool
	for _, rule := range r.Deny {
		if strings.Contains(rule, ".claude") {
			guards = true
		}
	}
	if !guards {
		t.Errorf("the defaults do not protect the box's own guide: %v", r.Deny)
	}
}

func TestAnEmptyDenyListMeansUnrestricted(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nroles:\n  trusted:\n    deny: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.Role("trusted")
	if err != nil {
		t.Fatal(err)
	}
	// Written on purpose, so it is honoured on purpose.
	if len(r.Deny) != 0 {
		t.Errorf("deny: [] was overridden with %v", r.Deny)
	}
}

func TestAKeyForAnUndefinedRoleIsAnError(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nroles:\n  known:\n    deny: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Role("unknown"); err == nil {
		t.Error("an undefined role resolved to something")
	}
	if _, err := cfg.Role(""); err == nil {
		t.Error("a key with no role resolved to something")
	}
}

// Roles were called permissions first. A box configured then must keep
// working, or an upgrade locks every caller out.
func TestTheOldSpellingIsStillRead(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\npermissions:\n  legacy:\n    deny: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Role("legacy"); err != nil {
		t.Errorf("a role written under the old key was lost: %v", err)
	}
}

// The API rewrites the commands section. It must not be able to take the
// roles with it — that is the whole reason keys are not in this file.
func TestWritingCommandsLeavesRolesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cbx.yaml")
	if err := os.WriteFile(path, []byte(
		"version: 1\nroles:\n  reporter:\n    deny: [\"Write(//x/**)\"]\ncommands: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteCommands(path, []Command{{Name: "/compact", Effect: Forward}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.Role("reporter")
	if err != nil {
		t.Fatalf("the role was lost when the commands were rewritten: %v", err)
	}
	if len(r.Deny) != 1 || r.Deny[0] != "Write(//x/**)" {
		t.Errorf("the role's rules changed: %v", r.Deny)
	}
	if len(cfg.Commands) != 1 || cfg.Commands[0].Name != "/compact" {
		t.Errorf("the commands were not replaced: %v", cfg.Commands)
	}
}
