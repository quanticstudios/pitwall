#!/bin/sh
# Checks a release's dist folder before gh release create: exactly the
# eight archives plus checksums.txt, one matching checksum line for each
# archive, and in each a binary whose Go build settings fit its name: GOOS
# and GOARCH, and the nogui tag with CGO_ENABLED=0 for pitwall-core and no
# nogui tag for pitwall. Needs go, tar and unzip.
#   sh scripts/release-check.sh <dist>
set -eu

if [ $# -ne 1 ]; then
    printf '%s\n' 'usage: release-check.sh <dist>' >&2
    exit 2
fi
dist=$1
names='pitwall-core_linux_amd64.tar.gz
pitwall-core_linux_arm64.tar.gz
pitwall_darwin_amd64.tar.gz
pitwall_darwin_arm64.tar.gz
pitwall_linux_amd64.tar.gz
pitwall_linux_arm64.tar.gz
pitwall_windows_amd64.zip
pitwall_windows_arm64.zip'
fail=0
bad() {
    printf 'release-check: %s\n' "$*" >&2
    fail=1
}

got=$(cd "$dist" && ls -A | grep -vx checksums.txt | LC_ALL=C sort)
want=$(printf '%s\n' "$names" | LC_ALL=C sort)
if [ "$got" != "$want" ]; then
    bad "$dist holds:
$got
want:
$want"
fi
if [ ! -f "$dist/checksums.txt" ]; then
    bad "$dist has no checksums.txt"
    exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
for name in $names; do
    f=$dist/$name
    [ -f "$f" ] || continue # reported above
    sums=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$dist/checksums.txt")
    if [ "$(printf '%s\n' "$sums" | grep -c .)" -ne 1 ]; then
        bad "checksums.txt needs one line for $name, not: $sums"
    elif [ "$sums" != "$(sha256sum "$f" | cut -d ' ' -f 1)" ]; then
        bad "$name does not match checksums.txt"
    fi

    base=${name%%.*} # pitwall-core_linux_amd64
    flavor=${base%%_*}
    rest=${base#*_}
    goos=${rest%%_*}
    goarch=${rest#*_}
    exe=$flavor
    [ "$goos" = windows ] && exe=$flavor.exe
    rm -rf "$tmp/x"
    mkdir "$tmp/x"
    case "$name" in
        *.zip) ok=$(unzip -q "$f" "$exe" -d "$tmp/x" 2>&1 && echo y) || true ;;
        *) ok=$(tar -xzf "$f" -C "$tmp/x" "$exe" 2>&1 && echo y) || true ;;
    esac
    if [ ! -f "$tmp/x/$exe" ]; then
        bad "$name has no $exe: $ok"
        continue
    fi
    if ! info=$(go version -m "$tmp/x/$exe" 2>&1); then
        bad "$name: $exe is not a Go binary: $info"
        continue
    fi
    # setting prints the value of build setting $1.
    setting() {
        printf '%s\n' "$info" | awk -v k="$1" '$1 == "build" && index($2, k "=") == 1 { print substr($2, length(k) + 2) }'
    }
    [ "$(setting GOOS)" = "$goos" ] || bad "$name holds a GOOS=$(setting GOOS) binary"
    [ "$(setting GOARCH)" = "$goarch" ] || bad "$name holds a GOARCH=$(setting GOARCH) binary"
    case "$flavor,$(setting -tags)," in
        pitwall-core,nogui,* | pitwall-core,*,nogui,*)
            [ "$(setting CGO_ENABLED)" = 0 ] || bad "$name holds a pitwall-core built with cgo"
            ;;
        pitwall-core,*) bad "$name holds a binary built without -tags nogui: the desktop pitwall" ;;
        pitwall,nogui,* | pitwall,*,nogui,*) bad "$name holds a binary built with -tags nogui: pitwall-core" ;;
    esac
done
exit "$fail"
