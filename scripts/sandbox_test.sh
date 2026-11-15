#!/bin/sh
# Checks that sandbox.sh passes on nothing of the caller's environment but
# TERM, and puts every path it sets under its folder: sh scripts/sandbox_test.sh
set -eu

here=$(cd "$(dirname -- "$0")" && pwd -P)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0

# Values a caller's shell could carry into a test daemon.
export CLAUDE_CONFIG_DIR=/real/claude CODEX_HOME=/real/codex PI_CODING_AGENT_DIR=/real/pi
export OPENCODE_CONFIG_DIR=/real/opencode XDG_RUNTIME_DIR=/real/run XDG_STATE_HOME=/real/state
export XDG_CONFIG_HOME=/real/config XDG_CACHE_HOME=/real/cache XDG_DATA_HOME=/real/data
export PITWALL_SOCKET=/real/pitwall.sock PITWALL_PANE=p1 PITWALL_ATTACH=1 PITWALL_GUARD=0
export SANDBOX_TEST_SECRET=leaked TERM=xterm-256color

# Inside the sandbox the environment holds only what sandbox.sh set, so
# listing its names shows nothing of the caller's. The shell adds the
# names it always sets.
sh "$here/sandbox.sh" "$tmp/box" -- /bin/sh -c 'env | sed "s/=.*//" | sort' > "$tmp/names"
want='CLAUDE_CONFIG_DIR CODEX_HOME HOME OPENCODE_CONFIG_DIR PATH PI_CODING_AGENT_DIR SHELL TERM XDG_CACHE_HOME XDG_CONFIG_HOME XDG_RUNTIME_DIR XDG_STATE_HOME '
got=$(grep -vxE 'PWD|OLDPWD|SHLVL|_' "$tmp/names" | tr '\n' ' ')
if [ "$got" != "$want" ]; then
    printf 'variables in the sandbox:\n  got  %s\n  want %s\n' "$got" "$want" >&2
    fail=1
fi

box=$(cd "$tmp/box" && pwd -P)
sh "$here/sandbox.sh" "$tmp/box" -- /bin/sh -c '
    for v in HOME XDG_RUNTIME_DIR XDG_STATE_HOME XDG_CONFIG_HOME XDG_CACHE_HOME CLAUDE_CONFIG_DIR CODEX_HOME PI_CODING_AGENT_DIR OPENCODE_CONFIG_DIR; do
        eval "printf \"%s %s\\n\" $v \"\$$v\""
    done
    printf "TERM %s\\nPATH %s\\n" "$TERM" "$PATH"' > "$tmp/values"
while read -r name value; do
    case "$name" in
        TERM) [ "$value" = xterm-256color ] || { printf 'TERM = %s\n' "$value" >&2; fail=1; } ;;
        PATH) [ "$value" = "$box/bin:/usr/bin:/bin" ] || { printf 'PATH = %s\n' "$value" >&2; fail=1; } ;;
        *)
            case "$value" in
                "$box"/*) [ -d "$value" ] || { printf '%s = %s is not a folder\n' "$name" "$value" >&2; fail=1; } ;;
                *) printf '%s = %s, outside %s\n' "$name" "$value" "$box" >&2; fail=1 ;;
            esac
            ;;
    esac
done < "$tmp/values"
if [ "$(wc -l < "$tmp/values")" -ne 11 ]; then
    printf 'values:\n%s\n' "$(cat "$tmp/values")" >&2
    fail=1
fi
if [ "$(stat -c %a "$box/claude" 2>/dev/null || stat -f %Lp "$box/claude")" != 700 ]; then
    printf '%s\n' 'the agent folders are not private' >&2
    fail=1
fi

# Reusing a sandbox must not follow a config or home symlink outside it,
# even to chmod the target. Check every directory the helper creates.
for d in bin home run state config cache claude codex pi opencode; do
    root=$tmp/link-$d
    outside=$tmp/outside-$d
    mkdir "$root" "$outside"
    chmod 755 "$outside"
    ln -s "$outside" "$root/$d"
    if sh "$here/sandbox.sh" "$root" -- /bin/sh -c 'touch "$1/ran"' sh "$outside" 2>/dev/null; then
        printf 'sandbox followed the %s symlink\n' "$d" >&2
        fail=1
    fi
    mode=$(stat -c %a "$outside" 2>/dev/null || stat -f %Lp "$outside")
    if [ -e "$outside/ran" ] || [ "$mode" != 755 ]; then
        printf 'sandbox touched the %s symlink target\n' "$d" >&2
        fail=1
    fi
done

# A bad call starts nothing.
for args in "$tmp/x" "/ -- true" "$tmp/x true"; do
    # shellcheck disable=SC2086 # split on purpose: each case is a word list
    if sh "$here/sandbox.sh" $args 2>/dev/null; then
        printf 'sandbox.sh %s ran\n' "$args" >&2
        fail=1
    fi
done
exit "$fail"
