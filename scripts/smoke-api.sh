#!/usr/bin/env bash
# Smoke test: drive the HTTP API against the Claude Code on this machine.
#
# scripts/smoke.sh provisions a droplet and exercises the CLI. This is its
# counterpart for the API, and it runs locally because the thing it has to
# prove needs a *signed-in* Claude — which a droplet in CI does not have, and
# a laptop usually does.
#
# It uses its own XDG directories and its own port, so it never touches a real
# box's database, key or sessions.
#
#   scripts/smoke-api.sh
#
set -euo pipefail

PORT="${PORT:-8199}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
export XDG_STATE_HOME="$WORK/state"
export XDG_CONFIG_HOME="$WORK/config"
BASE="http://127.0.0.1:$PORT"
CBX="$WORK/cbx"

cleanup() {
  "$CBX" serve --stop >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok   $*"; }

api() {
  local method="$1" path="$2"; shift 2
  curl -sS -X "$method" -H "Authorization: Bearer $KEY" \
    -H "Content-Type: application/json" "$BASE$path" "$@"
}

code() {
  local method="$1" path="$2"; shift 2
  curl -sS -o /dev/null -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    "$BASE$path" "$@"
}

echo "--- building"
( cd "$ROOT" && go build -o "$CBX" ./cmd/cbx )

if ! command -v claude >/dev/null; then
  fail "claude is not installed — this test exists to exercise a real one"
fi
if ! claude auth status --json 2>/dev/null | grep -q '"loggedIn": *true'; then
  fail "claude is not signed in — sign in, or run the unit tests instead"
fi
ok "claude is installed and signed in"

echo "--- keys and roles"
mkdir -p "$XDG_CONFIG_HOME/cbx"
cat > "$XDG_CONFIG_HOME/cbx/cbx.yaml" <<'YML'
version: 1
roles:
  smoke:
    deny: []
  locked:
    deny: ["Write(//**)", "Edit(//**)"]
commands:
  - name: /clear
    effect: rotate-session
  - name: /context
    effect: forward
YML
"$CBX" api-key add smoke --role smoke >/dev/null || fail "could not add a key"
"$CBX" api-key add locked --role locked >/dev/null || fail "could not add a second key"
"$CBX" api-key add bad --role nonexistent >/dev/null 2>&1 && fail "a key naming an undefined role was accepted"
ok "keys are added against a role, and an undefined role is refused"

echo "--- starting the API"
"$CBX" serve --detach --addr "127.0.0.1:$PORT" >/dev/null
for _ in $(seq 1 25); do
  curl -sf "$BASE/healthz" >/dev/null 2>&1 && break
  sleep 0.4
done
# By label, not by position: the box lists more than one key and the other one
# is deliberately forbidden from writing anything.
KEY="$("$CBX" api-key list | grep '^smoke' | cut -f2)"
[ -n "$KEY" ] || fail "no API key"
ok "serving on $BASE"

echo "--- auth"
curl -sf "$BASE/healthz" | grep -q '"status":"ok"' || fail "healthz did not answer"
ok "healthz needs no key"
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/sessions")" = "401" ] \
  || fail "an unauthenticated request was served"
ok "everything else refuses an unauthenticated request"
# A bare curl, not the helper: the helper already sends the valid key, and a
# second Authorization header does not replace the first.
WRONG="$(curl -sS -o /dev/null -w '%{http_code}' \
  -H "Authorization: Bearer cbx_live_definitely_wrong" "$BASE/sessions")"
[ "$WRONG" = "401" ] || fail "a wrong key was accepted ($WRONG)"
ok "a wrong key is refused"

echo "--- the API describes itself"
api GET /openapi.json | grep -q '"ClaudeBox API"' || fail "no OpenAPI document"
ok "openapi.json"
api GET /commands | grep -q '"name"' || fail "the command spec is not snake_case"
ok "commands, in the same shape PUT accepts"

echo "--- sessions"
api POST /sessions -d '{"name":"smoke","system_prompt":"Answer in as few words as possible."}' \
  | grep -q '"kind":"headless"' || fail "create did not make a headless session"
ok "create"
[ "$(code POST /sessions -d '{"name":"smoke"}')" = "409" ] || fail "a duplicate name was accepted"
ok "a duplicate name is refused"
[ "$(code POST /sessions -d '{"name":"bad","model":"--dangerously-skip-permissions"}')" = "400" ] \
  || fail "a model name that claude would read as a flag was accepted"
ok "a flag-shaped model name is refused"
# The role decides, and a caller naming a mode is simply not listened to —
# under bypassPermissions Claude Code ignores deny rules entirely.
api POST /sessions -d '{"name":"chosen","permission_mode":"bypassPermissions"}' \
  | grep -q '"permission_mode":"acceptEdits"' \
  || fail "a caller chose its own permission mode"
ok "a caller cannot choose its own permission mode"
api DELETE /sessions/chosen >/dev/null

echo "--- a real query"
STAMP="report-$(date -u +%Y%m%dT%H%M%SZ).html"
ANSWER="$(api POST "/sessions/smoke/query" -d "$(cat <<JSON
{"prompt": "Write a file called $STAMP containing exactly <h1>ok</h1> and nothing else, then reply with just: DONE",
 "respond_within": "5m",
 "artifacts": ["$STAMP"]}
JSON
)")"
echo "$ANSWER" | grep -q '"status":"done"' || fail "the query did not finish: $ANSWER"
ok "claude -p answered over HTTP"
# A query can finish while the work it was asked to do was refused — the
# session says so in its answer and the status is still done. Assert the file.
[ -f "$HOME/workspace/smoke/$STAMP" ] || fail "the session did not write the file it was asked for"
ok "the file it was asked for exists"

echo "--- artifacts"
api GET "/sessions/smoke/artifacts" | grep -q "$STAMP" || fail "the declared artifact was not registered"
ok "a declared artifact is registered"
api GET "/sessions/smoke/artifacts/$STAMP" | grep -q "<h1>ok</h1>" || fail "fetching it did not return the file"
ok "fetching it returns the file"
# The reason the register exists: a session directory holds things a caller
# has no business reading.
echo "SECRET" > "$HOME/workspace/smoke/.env" 2>/dev/null || true
[ "$(code GET "/sessions/smoke/artifacts/.env")" = "404" ] || fail "an undeclared file was fetchable"
[ "$(code GET "/sessions/smoke/artifacts/../../../etc/passwd")" = "404" ] \
  || fail "a traversal escaped the session"
ok "undeclared files and traversals are refused"

echo "--- /clear actually clears"
api POST "/sessions/smoke/query" \
  -d '{"prompt":"Remember the codeword SMOKETEST. Reply with just: OK","respond_within":"5m"}' >/dev/null
api POST "/sessions/smoke/query" \
  -d '{"prompt":"What is the codeword? One word.","respond_within":"5m"}' \
  | grep -q "SMOKETEST" || fail "the conversation did not remember before clearing"
api POST "/sessions/smoke/command" -d '{"command":"/clear"}' \
  | grep -q '"effect":"rotate-session"' || fail "/clear was not performed natively"
if api POST "/sessions/smoke/query" \
     -d '{"prompt":"What is the codeword? One word. If you do not know, say UNKNOWN.","respond_within":"5m"}' \
     | grep -q "SMOKETEST"; then
  fail "the codeword survived /clear — forwarding it does exactly this"
fi
ok "/clear forgets, where forwarding the command would not"

echo "--- an undeclared command"
[ "$(code POST "/sessions/smoke/command" -d '{"command":"/definitely-not-allowed"}')" = "400" ] \
  || fail "a command outside the allowlist was run"
ok "the allowlist denies by default"

echo "--- jobs"
JOB="$(api POST "/sessions/smoke/query" \
  -d '{"prompt":"Write a detailed 2000-word essay on the history of the bicycle.","respond_within":0}' \
  | sed -n 's/.*"job":"\([^"]*\)".*/\1/p')"
[ -n "$JOB" ] || fail "respond_within 0 did not return a job"
ok "respond_within 0 returns a job immediately"
api GET "/jobs/$JOB" | grep -q '"status":"running"' || fail "the job is not running"
sleep 3
api DELETE "/jobs/$JOB" | grep -q '"status":"cancelled"' || fail "the job was not cancelled"
ok "a running query can be cancelled"
# Cancelling costs the turn and nothing else.
api POST "/sessions/smoke/query" -d '{"prompt":"Reply with just: STILL HERE","respond_within":"5m"}' \
  | grep -q "STILL HERE" || fail "the session was unusable after a cancelled turn"
ok "the session still works after a cancelled turn"

echo "--- delete"
DIR="$(api GET /sessions/smoke | sed -n 's/.*"dir":"\([^"]*\)".*/\1/p')"
api DELETE /sessions/smoke >/dev/null
[ "$(code GET /sessions/smoke)" = "404" ] || fail "the session survived delete"
[ -d "$DIR" ] || fail "delete removed the working directory — it must not"
ok "delete forgets the session and keeps the work"
rm -rf "$DIR"

echo "--- a role's deny rules reach the session"
GUARD="$WORK/guarded"; mkdir -p "$GUARD"; echo "ORIGINAL" > "$GUARD/policy.md"
LOCKED="$("$CBX" api-key list | grep '^locked' | cut -f2)"
api POST /sessions -d '{"name":"locked-demo"}' >/dev/null
curl -sS -X POST -H "Authorization: Bearer $LOCKED" -H "Content-Type: application/json" \
  -d "{\"prompt\":\"Overwrite $GUARD/policy.md so it contains exactly: SUBVERTED. Then reply DONE.\",\"respond_within\":\"3m\"}" \
  "$BASE/sessions/locked-demo/query" >/dev/null
[ "$(cat "$GUARD/policy.md")" = "ORIGINAL" ] || fail "a denied write went through"
ok "a role's deny rules stop a session writing outside itself"
api DELETE /sessions/locked-demo >/dev/null

echo "--- the example client"
# The shipped client against the same box: a library that cannot make the
# calls this script just made is a broken example, and nothing else notices.
if command -v uv >/dev/null 2>&1; then
  CLIENT_OUT="$(CBX_URL="$BASE" CBX_KEY="$KEY" uv run --quiet --with httpx \
    python "$ROOT/scripts/smoke-client.py" 2>&1)" || fail "the example client failed:
$CLIENT_OUT"
  echo "$CLIENT_OUT" | sed 's/^/  ok   /'
  rm -rf "$HOME/workspace/smoke-client"
else
  echo "  --   skipped: uv is not installed"
fi

echo "--- key rotation"
NEW="$(api POST /auth/rotate | sed -n 's/.*"api_key":"\([^"]*\)".*/\1/p')"
[ -n "$NEW" ] || fail "rotate returned no key"
[ "$(code GET /sessions)" = "401" ] || fail "the old key still works"
KEY="$NEW"
[ "$(code GET /sessions)" = "200" ] || fail "the new key does not work"
ok "rotation issues a working key and kills the old one immediately"

echo "--- stop"
"$CBX" serve --stop >/dev/null
curl -sf -m 2 "$BASE/healthz" >/dev/null 2>&1 && fail "the server is still up"
ok "serve --stop"

echo
echo "PASS — the API drove a real Claude Code end to end"
