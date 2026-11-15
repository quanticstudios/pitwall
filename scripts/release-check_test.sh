#!/bin/sh
# Runs release-check.sh on a release built from scripts/testdata/hello, then
# on broken copies of it: sh scripts/release-check_test.sh
# Needs go, python3 (to write the zips) and unzip.
set -eu

here=$(cd "$(dirname -- "$0")" && pwd -P)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# hello builds the stand-in binary for goos and goarch into $tmp/bin:
# pitwall-core with the nogui tag, pitwall without.
hello() {
    out=$tmp/bin/$1_$2_$3
    tags=novulkan
    [ "$1" = pitwall-core ] && tags=nogui
    (cd "$here/testdata/hello" && CGO_ENABLED=0 GOOS=$2 GOARCH=$3 go build -tags "$tags" -o "$out" .)
}
# pack puts binary $2 in archive $1 of dist $3 under the name the release uses.
pack() {
    base=${1%%.*}
    flavor=${base%%_*}
    dest=$(cd "$3" && pwd -P)
    stage=$tmp/stage/$base
    rm -rf "$stage"
    mkdir -p "$stage"
    case "$1" in
        *.zip)
            cp "$2" "$stage/$flavor.exe"
            (cd "$stage" && python3 -m zipfile -c "$dest/$1" "$flavor.exe")
            ;;
        *)
            cp "$2" "$stage/$flavor"
            tar -czf "$dest/$1" -C "$stage" "$flavor"
            ;;
    esac
}
sums() { (cd "$1" && rm -f checksums.txt && sha256sum pitwall_* pitwall-core_* > checksums.txt); }

mkdir -p "$tmp/bin" "$tmp/good"
for t in pitwall-core:linux:amd64 pitwall-core:linux:arm64 pitwall:linux:amd64 pitwall:linux:arm64 \
    pitwall:darwin:amd64 pitwall:darwin:arm64 pitwall:windows:amd64 pitwall:windows:arm64; do
    IFS=: read -r flavor goos goarch << EOF
$t
EOF
    hello "$flavor" "$goos" "$goarch"
    ext=tar.gz
    [ "$goos" = windows ] && ext=zip
    pack "${flavor}_${goos}_${goarch}.$ext" "$tmp/bin/${flavor}_${goos}_${goarch}" "$tmp/good"
done
sums "$tmp/good"

fail=0
# expect runs release-check.sh on a copy of the good dist that $2 changes,
# and wants exit $1.
expect() {
    d=$tmp/case
    rm -rf "$d"
    cp -R "$tmp/good" "$d"
    (cd "$d" && eval "$2")
    code=0
    sh "$here/release-check.sh" "$d" > "$tmp/out" 2>&1 || code=$?
    if [ "$code" -ne "$1" ]; then
        printf 'release-check after %s: exit %s, want %s\n%s\n' "$2" "$code" "$1" "$(cat "$tmp/out")" >&2
        fail=1
    fi
}
expect 0 :
expect 1 'rm pitwall-core_linux_arm64.tar.gz'
expect 1 'rm pitwall_windows_amd64.zip'
expect 1 'echo x > pitwall_linux_386.tar.gz'
expect 1 'grep -v pitwall-core_linux_amd64 checksums.txt > s && mv s checksums.txt'
expect 1 'grep pitwall-core_linux_amd64 checksums.txt > s && cat s >> checksums.txt && rm s'
expect 1 'rm checksums.txt'
expect 1 'echo x >> pitwall_darwin_arm64.tar.gz'
# A swapped flavor, each way, and a binary for the wrong platform.
expect 1 'pack pitwall-core_linux_amd64.tar.gz "$tmp/bin/pitwall_linux_amd64" . && sums .'
expect 1 'pack pitwall_linux_amd64.tar.gz "$tmp/bin/pitwall-core_linux_amd64" . && sums .'
expect 1 'pack pitwall_linux_arm64.tar.gz "$tmp/bin/pitwall_linux_amd64" . && sums .'
expect 1 'pack pitwall_windows_arm64.zip "$tmp/bin/pitwall_linux_arm64" . && sums .'
expect 1 'tar -czf pitwall-core_linux_arm64.tar.gz -C "$tmp/stage" . && sums .'
exit "$fail"
