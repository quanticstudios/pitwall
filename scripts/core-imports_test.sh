#!/bin/sh
# Checks core-imports.sh against fake import lists: sh scripts/core-imports_test.sh
set -eu

check=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/core-imports.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail=0
ok='fmt
gioui.org/f32
gioui.org/io/key
gioui.org/io/pointer
gioui.org/internal/ops
github.com/quanticstudios/pitwall/internal/config'
printf '%s\n' "$ok" >"$tmp/ok"
sh "$check" "$tmp/ok" >/dev/null 2>&1 || { echo 'FAIL: rejected the pure-Go Gio packages' >&2; fail=1; }

for extra in gioui.org/widget gioui.org/app gioui.org/internal/gl gioui.org/io/keyboard \
    github.com/quanticstudios/pitwall/internal/ui/theme; do
    printf '%s\n%s\n' "$ok" "$extra" >"$tmp/bad"
    if sh "$check" "$tmp/bad" >"$tmp/out" 2>&1; then
        echo "FAIL: accepted $extra" >&2
        fail=1
    elif ! grep -qxF "$extra" "$tmp/out"; then
        echo "FAIL: did not name $extra" >&2
        fail=1
    fi
done
[ "$fail" = 0 ] && echo 'core-imports.sh: ok'
exit "$fail"
