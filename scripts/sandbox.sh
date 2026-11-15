#!/bin/sh
# Runs a command with a fresh environment whose home, XDG and agent folders
# all sit under one folder, so a pitwall daemon there touches no real
# config, socket or agent skill:
#   sh scripts/sandbox.sh <dir> -- <command> [args...]
# Only TERM comes from the caller. PATH is <dir>/bin, /usr/bin and /bin.
set -eu

if [ $# -lt 3 ] || [ "$2" != -- ]; then
    printf '%s\n' 'usage: sandbox.sh <dir> -- <command> [args...]' >&2
    exit 2
fi
mkdir -p "$1"
root=$(cd "$1" && pwd -P)
shift 2
# why: under / every path below would be a real system folder.
if [ "$root" = / ]; then
    printf '%s\n' 'sandbox.sh: the folder must not be /' >&2
    exit 2
fi
for d in bin home run state config cache claude codex pi opencode; do
    # why: a reused sandbox may have a link to real config. mkdir and
    # chmod both follow it, and the command would then write outside root.
    if [ -L "$root/$d" ]; then
        printf 'sandbox.sh: %s must not be a symlink\n' "$root/$d" >&2
        exit 2
    fi
    mkdir -p "$root/$d"
    chmod 700 "$root/$d"
done
exec env -i \
    HOME="$root/home" \
    XDG_RUNTIME_DIR="$root/run" \
    XDG_STATE_HOME="$root/state" \
    XDG_CONFIG_HOME="$root/config" \
    XDG_CACHE_HOME="$root/cache" \
    CLAUDE_CONFIG_DIR="$root/claude" \
    CODEX_HOME="$root/codex" \
    PI_CODING_AGENT_DIR="$root/pi" \
    OPENCODE_CONFIG_DIR="$root/opencode" \
    PATH="$root/bin:/usr/bin:/bin" \
    TERM="${TERM:-dumb}" \
    SHELL=/bin/sh \
    "$@"
