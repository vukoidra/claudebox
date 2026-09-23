package store

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"
)

// Who may call the API, and what their sessions are allowed to do.
//
// In the database rather than the config file, and the difference is not
// tidiness. The API rewrites the config file — that is what PUT /commands is —
// so a key stored there could be edited by the caller it is meant to
// constrain. Credentials live here; hand-edited policy lives in the yaml; the
// two never share a writer.

// KeyPrefix marks a cbx key, so one is recognisable when it turns up somewhere
// it should not be.
const KeyPrefix = "cbx_live_"

// APIKey is one caller.
//
// Permission is the *name* of a profile defined in the box's config file, not
// the policy itself. Keeping the policy in one hand-edited place means
// changing what a profile allows updates every key holding it, and a key row
// never has to be rewritten to tighten a rule.
type APIKey struct {
	Label      string
	Value      string
	Permission string
	CreatedAt  time.Time
}

// NewKeyValue mints a key. 32 bytes from crypto/rand, which is not guessable
// and not worth anybody's time to search.
func NewKeyValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// AddKey records a new key and returns its value.
//
// The value is returned once here. Afterwards it is only ever read back by
// somebody with access to this database, which is somebody with ssh.
func (s *Store) AddKey(label, permission string) (string, error) {
	if label == "" {
		return "", fmt.Errorf("a key needs a label")
	}
	if permission == "" {
		return "", fmt.Errorf("a key needs a permission profile")
	}
	value, err := NewKeyValue()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(
		`INSERT INTO keys (label, value, permission, created_at) VALUES (?,?,?,?)`,
		label, value, permission, s.now().Unix())
	if err != nil {
		return "", fmt.Errorf("add key %q: %w (a label is used once)", label, err)
	}
	return value, nil
}

// AdoptKey records a key whose value already exists.
//
// Used once, on upgrade: a box provisioned before keys were recorded has a
// value its callers are already using, and dropping it would lock them out at
// the moment nobody expected a change. Adopting an already-known value is not
// an error, so a restart does not fail on it.
func (s *Store) AdoptKey(label, value, permission string) (bool, error) {
	if existing, err := s.KeyByValue(value); err != nil {
		return false, err
	} else if existing != nil {
		return false, nil
	}
	if _, err := s.db.Exec(
		`INSERT INTO keys (label, value, permission, created_at) VALUES (?,?,?,?)`,
		label, value, permission, s.now().Unix()); err != nil {
		return false, fmt.Errorf("adopt key %q: %w", label, err)
	}
	return true, nil
}

// KeyByValue returns the key a presented value belongs to.
//
// Every row is compared in constant time and the loop is not cut short on a
// match, so how long this takes says nothing about which key matched or
// whether one did. A byte-at-a-time comparison would leak a key's prefix to
// anyone willing to time the responses.
func (s *Store) KeyByValue(presented string) (*APIKey, error) {
	all, err := s.Keys()
	if err != nil {
		return nil, err
	}
	var found *APIKey
	for i := range all {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(all[i].Value)) == 1 {
			found = &all[i]
		}
	}
	return found, nil
}

// Keys returns every key, oldest first.
func (s *Store) Keys() ([]APIKey, error) {
	rows, err := s.db.Query(
		`SELECT label, value, permission, created_at FROM keys ORDER BY created_at, label`)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		var k APIKey
		var created int64
		if err := rows.Scan(&k.Label, &k.Value, &k.Permission, &created); err != nil {
			return nil, fmt.Errorf("scan key: %w", err)
		}
		k.CreatedAt = time.Unix(created, 0)
		out = append(out, k)
	}
	return out, rows.Err()
}

// Key returns one by label. Absent is (nil, nil).
func (s *Store) Key(label string) (*APIKey, error) {
	var k APIKey
	var created int64
	err := s.db.QueryRow(
		`SELECT label, value, permission, created_at FROM keys WHERE label = ?`, label).
		Scan(&k.Label, &k.Value, &k.Permission, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read key %q: %w", label, err)
	}
	k.CreatedAt = time.Unix(created, 0)
	return &k, nil
}

// RotateKey replaces one key's value, leaving its permissions alone.
//
// The old value stops working on the next request. There is no grace period,
// because the reason to rotate is usually that it should already have stopped
// working.
func (s *Store) RotateKey(label string) (string, error) {
	value, err := NewKeyValue()
	if err != nil {
		return "", err
	}
	res, err := s.db.Exec(`UPDATE keys SET value = ? WHERE label = ?`, value, label)
	if err != nil {
		return "", fmt.Errorf("rotate key %q: %w", label, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("no key labelled %q", label)
	}
	return value, nil
}

// SetPermission moves a key to a different profile, without reissuing it.
func (s *Store) SetPermission(label, permission string) error {
	res, err := s.db.Exec(`UPDATE keys SET permission = ? WHERE label = ?`, permission, label)
	if err != nil {
		return fmt.Errorf("set permission for %q: %w", label, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no key labelled %q", label)
	}
	return nil
}

// RevokeKey removes a key. Whoever holds it stops working immediately, which
// is the point.
func (s *Store) RevokeKey(label string) error {
	res, err := s.db.Exec(`DELETE FROM keys WHERE label = ?`, label)
	if err != nil {
		return fmt.Errorf("revoke key %q: %w", label, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no key labelled %q", label)
	}
	return nil
}
