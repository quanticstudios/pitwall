#!/bin/sh
# Runs a pitwall-core binary once in a sandbox (sandbox.sh): its --version,
# a daemon, a tab whose program exits 3, a wait for that exit, then a stop
# that must leave no process behind:
#   sh scripts/core-smoke.sh <pitwall-core> [tag]
# With a tag, --version must print exactly "pitwall <tag> (core)".
set -eu

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
    printf '%s\n' 'usage: core-smoke.sh <pitwall-core> [tag]' >&2
    exit 2
fi
here=$(cd "$(dirname -- "$0")" && pwd -P)
bin=$(cd "$(dirname -- "$1")" && pwd -P)/$(basename -- "$1")
tag=${2:-}

fail() {
    printf 'core-smoke: %s\n' "$*" >&2
    exit 1
}

v=$("$bin" --version)
if [ -n "$tag" ]; then
    [ "$v" = "pitwall $tag (core)" ] || fail "--version printed '$v', want 'pitwall $tag (core)'"
else
    case "$v" in
        'pitwall '*' (core)') ;;
        *) fail "--version printed '$v', want 'pitwall <version> (core)'" ;;
    esac
fi

tmp=$(mktemp -d)
pids=
# stop ends every recorded process: SIGTERM, up to 10 s, then SIGKILL; it
# names any that needed the SIGKILL.
stop() {
    for p in $pids; do kill "$p" 2>/dev/null || true; done
    i=0
    while [ $i -lt 100 ]; do
        alive=
        for p in $pids; do kill -0 "$p" 2>/dev/null && alive="$alive $p"; done
        [ -z "$alive" ] && break
        sleep 0.1
        i=$((i + 1))
    done
    if [ -n "$alive" ]; then
        for p in $alive; do kill -9 "$p" 2>/dev/null || true; done
        printf 'core-smoke: killed%s, which outlived SIGTERM by 10 s\n' "$alive" >&2
        return 1
    fi
}
cleanup() {
    stop || true
    rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

core() { sh "$here/sandbox.sh" "$tmp" -- "$bin" "$@"; }

sh "$here/sandbox.sh" "$tmp" -- "$bin" daemon > "$tmp/daemon.out" 2>&1 &
daemon=$!
pids=$daemon
i=0
until core ls > /dev/null 2>&1; do
    kill -0 "$daemon" 2>/dev/null || fail "the daemon exited: $(cat "$tmp/daemon.out")"
    [ $i -lt 100 ] || fail 'the daemon did not answer within 10 s'
    sleep 0.1
    i=$((i + 1))
done

# The pane records its pid in the sandbox's home, then exits 3.
core new -d -n smoke -- /bin/sh -c 'echo $$ > "$HOME/pane.pid"; sleep 1; exit 3' > /dev/null
i=0
until [ -s "$tmp/home/pane.pid" ]; do
    [ $i -lt 100 ] || fail 'the tab did not start within 10 s'
    sleep 0.1
    i=$((i + 1))
done
pids="$pids $(cat "$tmp/home/pane.pid")"
code=0
out=$(core wait smoke --until exit --timeout 30s 2>&1) || code=$?
[ $code -eq 3 ] || fail "wait --until exit returned $code, want the program's 3: $out"

stop || fail 'a process outlived the stop'
pids=
printf 'core-smoke: %s ran a daemon and a tab, and stopped cleanly.\n' "$v"
