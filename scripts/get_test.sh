#!/bin/sh
# Checks get.sh's platform to asset mapping, and the pitwall link next to
# pitwall-core: sh scripts/get_test.sh
set -eu

export PITWALL_GET_TEST=1
# shellcheck source=scripts/get.sh
. "$(dirname -- "$0")/get.sh"

fail=0
check() {
    got=$(asset "$1" "$2" "$3" 2>/dev/null) || got=error
    if [ "$got" != "$4" ]; then
        printf 'asset %s %s %s = %s, want %s\n' "$1" "$2" "$3" "$got" "$4" >&2
        fail=1
    fi
}
check Linux x86_64 pitwall pitwall_linux_amd64.tar.gz
check Linux aarch64 pitwall pitwall_linux_arm64.tar.gz
check Darwin arm64 pitwall pitwall_darwin_arm64.tar.gz
check Darwin x86_64 pitwall pitwall_darwin_amd64.tar.gz
check Linux armv7l pitwall error
check MINGW64_NT-10.0 x86_64 pitwall error
check Linux x86_64 pitwall-core pitwall-core_linux_amd64.tar.gz
check Linux aarch64 pitwall-core pitwall-core_linux_arm64.tar.gz
check Darwin arm64 pitwall-core error
check Darwin x86_64 pitwall-core error
check Linux armv7l pitwall-core error
if msg=$(asset Darwin arm64 pitwall-core 2>&1); [ "$msg" != 'pitwall: pitwall-core is released for Linux only; leave out --core to install pitwall' ]; then
    printf 'pitwall-core on macOS says %s\n' "$msg" >&2
    fail=1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# linked reports whether $1/pitwall is a link to pitwall-core.
linked() { [ -L "$1/pitwall" ] && [ "$(readlink "$1/pitwall")" = pitwall-core ]; }

# The name is free: the link is made.
mkdir "$tmp/free"
out=$(link_core "$tmp/free")
if ! linked "$tmp/free" || [ "$out" != "Linked $tmp/free/pitwall to pitwall-core." ]; then
    printf 'free name: %s\n' "$out" >&2
    fail=1
fi
# The link is there from an earlier install: it stays, quietly.
out=$(link_core "$tmp/free")
if ! linked "$tmp/free" || [ -n "$out" ]; then
    printf 'existing link: %s\n' "$out" >&2
    fail=1
fi
# A real pitwall, and a link elsewhere, stay untouched, with the command
# that would replace them.
mkdir "$tmp/real" "$tmp/other"
printf 'desktop' > "$tmp/real/pitwall"
ln -s /opt/pitwall/pitwall "$tmp/other/pitwall"
for d in real other; do
    out=$(link_core "$tmp/$d")
    want="$tmp/$d/pitwall is another program, so it stays. To run pitwall-core as pitwall instead:
  ln -sf pitwall-core '$tmp/$d/pitwall'"
    if [ "$out" != "$want" ]; then
        printf '%s pitwall: %s\n' "$d" "$out" >&2
        fail=1
    fi
done
if [ "$(cat "$tmp/real/pitwall")" != desktop ] || [ -L "$tmp/real/pitwall" ] ||
    [ "$(readlink "$tmp/other/pitwall")" != /opt/pitwall/pitwall ]; then
    printf '%s\n' 'link_core changed a pitwall that is not its link' >&2
    fail=1
fi

# main --core end to end, with curl serving a release from $tmp/rel.
if [ "$(uname -s)" = Linux ]; then
    name=$(asset Linux "$(uname -m)" pitwall-core)
    mkdir -p "$tmp/rel/stage"
    printf '#!/bin/sh\necho "pitwall v9.9.9 (core)"\n' > "$tmp/rel/stage/pitwall-core"
    chmod 755 "$tmp/rel/stage/pitwall-core"
    tar -czf "$tmp/rel/$name" -C "$tmp/rel/stage" pitwall-core
    (cd "$tmp/rel" && printf '%s  %s\n' "$(sha256 "$name")" "$name" > checksums.txt)
    rel=$tmp/rel
    # why: main sets its own tmp.
    curl() { cp "$rel/${4##*/}" "$3"; }
    out=$(PITWALL_INSTALL_DIR=$tmp/bin main --core 2>&1) || fail=1
    if ! linked "$tmp/bin" || [ "$("$tmp/bin/pitwall" --version)" != 'pitwall v9.9.9 (core)' ]; then
        printf 'main --core:\n%s\n' "$out" >&2
        fail=1
    fi
    case "$out" in
        *"Installed pitwall v9.9.9 (core) in $tmp/bin."*"Linked $tmp/bin/pitwall to pitwall-core."*) ;;
        *) printf 'main --core said:\n%s\n' "$out" >&2; fail=1 ;;
    esac
    if (main --bogus) 2>/dev/null; then
        printf '%s\n' 'main took an unknown option' >&2
        fail=1
    fi
fi
exit "$fail"
