package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Context is how a session gets the knowledge it needs before its first turn.
// cbx knows nothing about what the files mean — only that they are under the
// box's Claude directory and that the session must have them.

// knowledge writes files into the box's ~/.claude, as cbx-setuptool migrate
// would, and returns a harness pointed at it.
func knowledge(t *testing.T, files map[string]string) *harness {
	t.Helper()
	h := answering(t, answers("s", "hi"))
	for rel, body := range files {
		p := filepath.Join(h.KnowledgeRoot(), rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func claudeMD(t *testing.T, h *harness, session string) string {
	t.Helper()
	sess, err := h.Store.Get(session)
	if err != nil || sess == nil {
		t.Fatalf("no session %q: %v", session, err)
	}
	body, err := os.ReadFile(filepath.Join(sess.Dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("no CLAUDE.md: %v", err)
	}
	return string(body)
}

func TestContextIsCopiedIntoTheSession(t *testing.T) {
	h := knowledge(t, map[string]string{
		"domains/billing.md":   "# Billing\nInvoices settle net-30.",
		"clients/northwind.md": "# Northwind\nAccount code NW-77.",
	})
	w := h.do("POST", "/sessions", map[string]any{
		"name": "report", "context": []string{"domains/billing.md", "clients/northwind.md"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d: %s", w.Code, w.Body)
	}
	// CLAUDE.md rather than the system prompt, because Claude Code snapshots
	// the system prompt on the first request and ignores later changes, while
	// CLAUDE.md is re-read every turn.
	got := claudeMD(t, h, "report")
	for _, want := range []string{"Invoices settle net-30", "Account code NW-77", "domains/billing.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("CLAUDE.md is missing %q:\n%s", want, got)
		}
	}
}

func TestContextIsRecordedOnTheSession(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "x"})
	h.do("POST", "/sessions", map[string]any{"name": "rec", "context": []string{"domains/x.md"}})

	// "What did this report know" has to have an answer after the fact.
	got, _ := h.Store.Get("rec")
	if got == nil || len(got.Context) != 1 || got.Context[0] != "domains/x.md" {
		t.Fatalf("context = %v", got)
	}
	view := h.json(h.do("GET", "/sessions/rec", nil))
	list, _ := view["context"].([]any)
	if len(list) != 1 || list[0] != "domains/x.md" {
		t.Errorf("the session does not report its context: %v", view["context"])
	}
}

func TestAnAbsentContextFileIsRefusedBeforeTheSessionExists(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "x"})
	w := h.do("POST", "/sessions", map[string]any{
		"name": "nope", "context": []string{"domains/does-not-exist.md"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
	// A session created without the knowledge it asked for only shows up later,
	// in what it writes.
	if got, _ := h.Store.Get("nope"); got != nil {
		t.Error("the session was created anyway")
	}
}

func TestAContextPathCannotEscapeTheKnowledgeDirectory(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "x"})
	secret := filepath.Join(t.TempDir(), "secret.md")
	os.WriteFile(secret, []byte("PRIVATE"), 0o600)
	os.Symlink(secret, filepath.Join(h.KnowledgeRoot(), "sneaky.md"))

	for _, bad := range []string{"../../../etc/passwd", "/etc/passwd", "sneaky.md", ""} {
		w := h.do("POST", "/sessions", map[string]any{
			"name": "esc", "context": []string{bad},
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("context %q: code = %d, want 400", bad, w.Code)
		}
	}
}

func TestADirectoryIsNotContext(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "x"})
	w := h.do("POST", "/sessions", map[string]any{"name": "dir", "context": []string{"domains"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400 — name the files, not the directory", w.Code)
	}
}

// A cloned repository usually brings its own CLAUDE.md, and it is the
// project's, not ours.
func TestAnExistingClaudeMDSurvives(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "domain knowledge"})
	dir := filepath.Join(h.t.TempDir(), "with-existing")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# The project's own rules\nRun the tests."), 0o644)

	if err := h.writeContext(dir, []string{"domains/x.md"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	for _, want := range []string{"The project's own rules", "Run the tests.", "domain knowledge"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

// Writing twice replaces rather than accumulates — a directory outlives the
// session that used it, because DELETE keeps the work.
func TestWritingContextTwiceDoesNotStack(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/a.md": "AAA", "domains/b.md": "BBB"})
	dir := filepath.Join(h.t.TempDir(), "twice")
	os.MkdirAll(dir, 0o755)

	if err := h.writeContext(dir, []string{"domains/a.md"}); err != nil {
		t.Fatal(err)
	}
	if err := h.writeContext(dir, []string{"domains/b.md"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if strings.Count(string(got), contextStart) != 1 {
		t.Errorf("the context block was written twice:\n%s", got)
	}
	if strings.Contains(string(got), "AAA") {
		t.Errorf("the first context survived the second:\n%s", got)
	}
	if !strings.Contains(string(got), "BBB") {
		t.Errorf("the second context is missing:\n%s", got)
	}
}

func TestNoContextWritesNoClaudeMD(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "x"})
	h.do("POST", "/sessions", map[string]any{"name": "bare"})
	sess, _ := h.Store.Get("bare")
	if _, err := os.Stat(filepath.Join(sess.Dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("a session with no context got a CLAUDE.md anyway")
	}
}

// Context persists for the life of the session, not just its first turn.
//
// It lives in the session's CLAUDE.md, which Claude Code re-reads on every
// turn — verified against 2.1.236, where rewriting CLAUDE.md between turns
// changed the answer while a changed --append-system-prompt did not.
func TestContextSurvivesTheConversationBeingCleared(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "The site code is AC-77."})
	writeSpec(t, h, "version: 1\ncommands:\n  - name: /clear\n    effect: rotate-session\n")
	h.do("POST", "/sessions", map[string]any{"name": "keep", "context": []string{"domains/x.md"}})

	before := claudeMD(t, h, "keep")
	if w := h.do("POST", "/sessions/keep/command", map[string]any{"command": "/clear"}); w.Code != http.StatusOK {
		t.Fatalf("/clear: %d %s", w.Code, w.Body)
	}

	// /clear starts a new conversation. The knowledge is a file in the
	// session's directory, so the new conversation has it too.
	if after := claudeMD(t, h, "keep"); after != before {
		t.Errorf("clearing the conversation changed the session's context:\n%s", after)
	}
	got, _ := h.Store.Get("keep")
	if got == nil || len(got.Context) != 1 {
		t.Errorf("the recorded context did not survive: %v", got)
	}
}

// And across a restart: nothing about it lives in the server's memory.
func TestContextSurvivesAServerRestart(t *testing.T) {
	h := knowledge(t, map[string]string{"domains/x.md": "persisted knowledge"})
	h.do("POST", "/sessions", map[string]any{"name": "restarted", "context": []string{"domains/x.md"}})

	sess, _ := h.Store.Get("restarted")
	body, err := os.ReadFile(filepath.Join(sess.Dir, "CLAUDE.md"))
	if err != nil || !strings.Contains(string(body), "persisted knowledge") {
		t.Fatalf("context is not on disk: %v", err)
	}
	// The row carries it too, so `cbx export db` answers "what did this
	// session know" without opening the directory.
	if len(sess.Context) != 1 || sess.Context[0] != "domains/x.md" {
		t.Errorf("context = %v", sess.Context)
	}
}
