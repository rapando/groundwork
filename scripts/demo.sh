#!/usr/bin/env bash
# End-to-end check of a clean install: install from release archives with
# install.sh, then on the demo repo (examples/app-with-infra) run init, check,
# doctor, then start the service with the demo as a project and drive
# plan → approve → apply → drift through that project's API.
# Fails if anything breaks or the whole thing takes more than 5 minutes.
#
#   make release-snapshot && scripts/demo.sh        # uses ./dist
#   DIST=/path/to/release/files scripts/demo.sh     # or any folder/URL with the release files
#
# Needs terraform (>= 1.4) and python3 on PATH; ansible is optional.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
start=$(date +%s)
step() { printf '\n== %s (%ss)\n' "$*" "$(( $(date +%s) - start ))"; }
fail() { printf 'DEMO FAILED: %s\n' "$*" >&2; exit 1; }

dist="${DIST:-$here/dist}"
case "$dist" in http*://* | file://*) base="$dist" ;; *) base="file://$dist" ;; esac
version="${GROUNDWORK_VERSION:-}"
if [ -z "$version" ] && [ -f "$dist/metadata.json" ]; then
  version="v$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$dist/metadata.json")"
fi
[ -n "$version" ] || fail "set GROUNDWORK_VERSION or DIST to a goreleaser dist/ folder"

work="$(mktemp -d)"
trap 'if [ -n "${server:-}" ]; then kill "$server" 2>/dev/null || true; fi; rm -rf "$work"' EXIT

step "install $version with install.sh"
GROUNDWORK_DOWNLOAD_BASE="$base" GROUNDWORK_VERSION="$version" GROUNDWORK_INSTALL_DIR="$work/bin" sh "$here/install.sh"
gw="$work/bin/groundwork"
"$gw" version

step "demo repository"
cp -R "$here/examples/app-with-infra" "$work/demo"
cd "$work/demo"
git init -q && git add -A && git -c user.name=demo -c user.email=demo@example.com commit -qm "demo"

step "groundwork init --detect --yes"
"$gw" init --detect --yes
grep -q "deploy/terraform" groundwork.yaml || fail "detection missed deploy/terraform"

step "groundwork check"
"$gw" check || [ $? -eq 3 ] || fail "check found problems in the demo repo"

step "groundwork doctor"
"$gw" doctor || true # missing optional tools are warnings; a fail here is shown, not fatal

step "service, with the demo as a project"
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
# foreground, with its own data dir, so the run can't touch a real service
export GROUNDWORK_HOME="$work/home"
GROUNDWORK_TIPS=off "$gw" serve --no-open --port "$port" . >"$work/server.log" 2>&1 &
server=$!
for _ in $(seq 100); do grep -q '?t=' "$work/server.log" 2>/dev/null && break; sleep 0.1; done
url=$(grep -o 'http://[^ ]*/p/[a-z0-9-]*/?t=[0-9a-f]*' "$work/server.log") || fail "service didn't start: $(cat "$work/server.log")"
token="${url##*t=}"
project=$(printf '%s' "$url" | sed -E 's|.*/p/([a-z0-9-]+)/.*|\1|')
origin="http://127.0.0.1:$port"
curl -fsS -c "$work/jar" -o /dev/null "$url"
api() { # method path [json]: the demo project's API
  curl -fsS -b "$work/jar" -H "X-Groundwork-Token: $token" -H "Origin: $origin" -H 'Content-Type: application/json' \
    -X "$1" "$origin/api/p/$project$2" ${3:+--data "$3"}
}
"$gw" projects | grep -q "$project" || fail "groundwork projects doesn't list $project"
field() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {}, {'d': d}))" "$1"; }
wait_run() { # id status...
  local id=$1; shift
  for _ in $(seq 600); do
    st=$(api GET "/runs/$id" | field 'd["run"]["status"]')
    for want in "$@"; do [ "$st" = "$want" ] && return 0; done
    case "$st" in failed | cancelled) api GET "/runs/$id/log" | head -c 3000; fail "run $id $st" ;; esac
    sleep 0.5
  done
  fail "run $id stuck in $st"
}

step "environments"
api GET /envs | field '[e["name"] for e in d["envs"]]'

step "plan dev"
id=$(api POST /runs '{"kind":"tf.plan","root":"deploy/terraform","env":"dev"}' | field 'd["run"]["id"]')
wait_run "$id" waiting_approval
api GET "/runs/$id/plan" | field '"plan: %s" % d["plan"]["counts"] if "counts" in d["plan"] else "plan ready"'

step "approve and apply"
api POST "/runs/$id/approve" "{\"confirm_text\":\"\"}" >/dev/null
wait_run "$id" succeeded
api GET "/runs/$id" | field 'd["run"]["status"]'

step "drift (refresh-only)"
did=$(api POST /runs '{"kind":"tf.drift","root":"deploy/terraform","env":"dev"}' | field 'd["run"]["id"]')
wait_run "$did" succeeded
api GET "/drift?root=deploy/terraform&env=dev" | field '"%d drifted attributes" % len(d.get("drift") or d.get("rows") or [])'

step "issues and checks"
api GET /issues | field '"%d open issues" % len(d["open"])'
api GET /checks | field '"%d errors, %d warnings" % (d["counts"]["error"], d["counts"]["warning"])'

elapsed=$(( $(date +%s) - start ))
[ "$elapsed" -le 300 ] || fail "took ${elapsed}s (budget 300s)"
printf '\nDEMO OK in %ss\n' "$elapsed"
