#!/bin/sh
# Checks get.sh's platform to asset mapping: sh scripts/get_test.sh
set -eu

export PITWALL_GET_TEST=1
# shellcheck source=scripts/get.sh
. "$(dirname -- "$0")/get.sh"

fail=0
check() {
    got=$(asset "$1" "$2" 2>/dev/null) || got=error
    if [ "$got" != "$3" ]; then
        printf 'asset %s %s = %s, want %s\n' "$1" "$2" "$got" "$3" >&2
        fail=1
    fi
}
check Linux x86_64 pitwall_linux_amd64.tar.gz
check Linux aarch64 pitwall_linux_arm64.tar.gz
check Darwin arm64 pitwall_darwin_arm64.tar.gz
check Darwin x86_64 pitwall_darwin_amd64.tar.gz
check Linux armv7l error
check MINGW64_NT-10.0 x86_64 error
got=$(printf '[\n  {\n    "url": "x",\n    "tag_name": "v0.1.0-alpha.2",\n    "name": "pitwall"\n  },\n  {\n    "tag_name": "v0.1.0-alpha.1"\n  }\n]\n' | newest_tag)
if [ "$got" != v0.1.0-alpha.2 ]; then
    printf 'newest_tag = %s, want v0.1.0-alpha.2\n' "$got" >&2
    fail=1
fi
exit "$fail"
