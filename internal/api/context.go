package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Giving a session the knowledge it needs before its first turn.
//
// A box accumulates documents that only some sessions should know about — one
// per domain, one per customer, whatever the operator's structure is. `context`
// names which of them this session gets.
//
// cbx knows nothing about that structure. It resolves paths under the box's
// Claude directory and copies what they name into the session's CLAUDE.md;
// whether those paths spell `domains/billing.md` or something else entirely
// is the operator's business, and adding it to the API would make one
// organisation's taxonomy part of everybody's tool.
//
// CLAUDE.md rather than --append-system-prompt, and measured rather than
// assumed. Claude Code snapshots the system prompt on a conversation's first
// request and reuses it verbatim on every resume, so a system prompt set after
// that is silently ignored. CLAUDE.md is re-read every turn.

// contextMarker delimits what cbx wrote, so a session directory that already
// had a CLAUDE.md — a cloned repository usually does — keeps it, and creating
// over the same directory twice does not stack duplicates.
const (
	contextStart = "<!-- cbx:context -->"
	contextEnd   = "<!-- /cbx:context -->"
)

// KnowledgeRoot is where context paths resolve from: the same directory
// cbx-setuptool migrates skills, rules and anything else into.
func (s *Server) KnowledgeRoot() string {
	if s.Home == "" {
		return ""
	}
	return filepath.Join(s.Home, ".claude")
}

// resolveContext checks the files a caller named, before the session exists.
//
// A path that escapes the knowledge directory, names a directory, or is simply
// absent is refused here — the alternative is a session created without the
// knowledge it was asked for, which only shows up later in what it writes.
func (s *Server) resolveContext(paths []string) ([]string, error) {
	root := s.KnowledgeRoot()
	if len(paths) > 0 && root == "" {
		return nil, fmt.Errorf("this box has no knowledge directory, so context cannot be resolved")
	}
	out := make([]string, 0, len(paths))
	for _, rel := range paths {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			return nil, fmt.Errorf("a context path cannot be empty")
		}
		full, err := resolveInside(root, rel)
		if err != nil {
			return nil, fmt.Errorf("context %q: %w", rel, err)
		}
		info, err := os.Stat(full)
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("context %q is not on this box — `cbx export rules` lists what is", rel)
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("context %q is a directory; name the files you want", rel)
		}
		out = append(out, filepath.ToSlash(filepath.Clean(rel)))
	}
	return out, nil
}

// writeContext copies the named knowledge into the session's CLAUDE.md.
//
// Copied rather than imported by reference, so the session directory records
// what the session actually knew. A report produced last week is explicable
// from its own directory even after the domain document has moved on.
func (s *Server) writeContext(dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	root := s.KnowledgeRoot()

	var block strings.Builder
	block.WriteString(contextStart + "\n")
	block.WriteString("<!-- Copied by cbx when this session was created. Edit the\n")
	block.WriteString("     originals under ~/.claude and create a new session. -->\n\n")
	for _, rel := range paths {
		full, err := resolveInside(root, rel)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("read context %q: %w", rel, err)
		}
		fmt.Fprintf(&block, "## %s\n\n%s\n\n", rel, strings.TrimRight(string(content), "\n"))
	}
	block.WriteString(contextEnd + "\n")

	path := filepath.Join(dir, "CLAUDE.md")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// A cloned repository's own CLAUDE.md is the project's, not ours.
	merged := strings.TrimRight(stripContext(string(existing)), "\n")
	if merged != "" {
		merged += "\n\n"
	}
	return os.WriteFile(path, []byte(merged+block.String()), 0o644)
}

// stripContext removes a block cbx wrote earlier, so writing twice replaces
// rather than accumulates.
func stripContext(doc string) string {
	start := strings.Index(doc, contextStart)
	if start < 0 {
		return doc
	}
	end := strings.Index(doc, contextEnd)
	if end < 0 || end < start {
		return doc[:start]
	}
	return doc[:start] + doc[end+len(contextEnd):]
}
