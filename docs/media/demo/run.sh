#!/bin/sh
set -eu
pitwall=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
demo=$(cd "$(dirname "$0")" && pwd)
R=${PWMEDIA:-/tmp/pwmedia}
[ -f "$R/daemon.pid" ] && kill "$(cat "$R/daemon.pid")" 2>/dev/null && sleep 1
rm -rf "$R"
mkdir -p "$R/home" "$R/run" "$R/bin" "$R/config" "$R/data" "$R/state" "$R/cache"
chmod 700 "$R/run"
cp "$pitwall" "$R/bin/pitwall"
cp "$demo/claude" "$R/bin/claude"

export HOME="$R/home" XDG_RUNTIME_DIR="$R/run" XDG_CONFIG_HOME="$R/config" XDG_DATA_HOME="$R/data"
export XDG_STATE_HOME="$R/state" XDG_CACHE_HOME="$R/cache" CLAUDE_CONFIG_DIR="$R/home/.claude"
export PATH="$R/bin:/usr/bin:/bin" GIT_AUTHOR_NAME=demo GIT_AUTHOR_EMAIL=demo@example.com
export GIT_COMMITTER_NAME=demo GIT_COMMITTER_EMAIL=demo@example.com
unset PITWALL_SOCKET PITWALL_PANE PITWALL_ATTACH
mkdir -p "$CLAUDE_CONFIG_DIR"
printf 'PS1="%%1~ %%# "\n' >"$HOME/.zshrc"
printf '{"effortLevel":"high"}\n' >"$CLAUDE_CONFIG_DIR/settings.json"
mkdir -p "$XDG_CONFIG_HOME/pitwall" && : >"$XDG_CONFIG_HOME/pitwall/config.toml"

repo="$HOME/src/api"
python3 -I "$demo/chat.py" "$R/transcripts" "$HOME/src/api-webhook-retry" "$HOME/src/api-failed-deliveries-page"
mkdir -p "$repo/src/server" "$repo/web/src/pages"
cp -r "$R/transcripts/repo/." "$repo/"
cd "$repo"
git init -q -b main
printf '{"name":"api","version":"0.9.0"}\n' >package.json
printf 'export const router = {}\n' >src/server/router.ts
printf 'export {}\n' >web/src/pages/index.ts
git add -A && git commit -qm "Start the webhook service"
wt() { git worktree add -q "$HOME/src/api-$1" -b "$1"; }
wt webhook-retry
wt failed-deliveries-page
wt rate-limits
wt invoice-pdf
cp -r "$R/transcripts/retry/." "$HOME/src/api-webhook-retry/"
printf 'export function FailedDeliveries() {}\n' >"$HOME/src/api-failed-deliveries-page/web/src/pages/Failed.tsx"
printf 'export const limit = 100\n%.0s' 1 2 3 4 5 6 7 8 >"$HOME/src/api-rate-limits/src/server/rateLimit.ts"

setsid "$R/bin/pitwall" daemon </dev/null >"$R/daemon.log" 2>&1 &
echo $! >"$R/daemon.pid"
i=0
until "$R/bin/pitwall" ls >/dev/null 2>&1; do
	i=$((i + 1))
	[ $i -lt 50 ] || { echo "daemon did not start; see $R/daemon.log" >&2; exit 1; }
	sleep 0.2
done

new() {
	name=$1 dir=$2
	shift 2
	"$R/bin/pitwall" new -n "$name" "$dir" -- env "$@" claude >/dev/null
}
new "Retry failed webhooks" "$HOME/src/api-webhook-retry" DEMO_STATE=done \
	DEMO_TRANSCRIPT="$R/transcripts/webhook-retry.jsonl" DEMO_PROMPT="Retry failed webhooks with backoff" \
	DEMO_REPLY="Failed deliveries now retry with exponential backoff and full jitter."
new "Failed deliveries page" "$HOME/src/api-failed-deliveries-page" DEMO_STATE=working \
	DEMO_TRANSCRIPT="$R/transcripts/failed-page.jsonl" DEMO_PROMPT="Add a Failed deliveries page"
new "Add rate limits" "$HOME/src/api-rate-limits" DEMO_STATE=approval DEMO_PROMPT="Add rate limits to the API"
new "Queue for invoice PDFs" "$HOME/src/api-invoice-pdf" DEMO_STATE=input DEMO_PROMPT="Render invoices as PDFs"
"$R/bin/pitwall" new -n "npm run dev" "$repo" -- sh -c 'printf "  VITE v6.2.0  ready in 412 ms\n\n  Local:   http://localhost:3010/\n"; exec cat' >/dev/null
echo "daemon pid $(cat "$R/daemon.pid"); stop it with: kill \$(cat $R/daemon.pid)"
echo "open the window from a clean shell, never one with these XDG variables:"
echo "  cd $repo && agent-ws run pwmedia -- env HOME=$HOME XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR XDG_CONFIG_HOME=$XDG_CONFIG_HOME XDG_DATA_HOME=$XDG_DATA_HOME XDG_STATE_HOME=$XDG_STATE_HOME XDG_CACHE_HOME=$XDG_CACHE_HOME CLAUDE_CONFIG_DIR=$CLAUDE_CONFIG_DIR PATH=$PATH PITWALL_TEST_MAIN=1 PITWALL_TEST_SIZE=1600x800 <pitwall.test>"
