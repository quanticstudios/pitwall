#!/bin/sh
# Fails when pitwall-core imports window code: a Gio package other than the
# pure-Go input ones config, input and remote use, or any internal/ui one.
#   sh scripts/core-imports.sh         checks go list -deps of the core build
#   sh scripts/core-imports.sh FILE    checks FILE, one import path per line
set -eu

if [ $# -gt 0 ]; then
    deps=$(cat "$1")
else
    deps=$(CGO_ENABLED=0 go list -deps -tags nogui ./cmd/pitwall)
fi
allowed='gioui\.org/(f32|op|io/(event|key|pointer)|internal/(byteslice|f32|ops|scene))'
bad=$(printf '%s\n' "$deps" | grep -E '^gioui\.org/|/internal/ui(/|$)' | grep -vxE "$allowed" || true)
if [ -n "$bad" ]; then
    printf 'pitwall-core must not import:\n%s\n' "$bad" >&2
    exit 1
fi
echo 'pitwall-core imports no window code.'
