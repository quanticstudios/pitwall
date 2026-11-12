#!/bin/sh
set -eu

optional=
if [ "${1:-}" = --if-available ]; then optional=1; shift; fi
[ "$#" = 1 ] || { echo 'usage: scripts/apk-release.sh [--if-available] vX.Y.Z' >&2; exit 2; }
tag=$1
root=$(git rev-parse --show-toplevel)
common=$(git rev-parse --path-format=absolute --git-common-dir)
key=${PITWALL_APK_KEY:-$(dirname "$common")/dist/android/debug.keystore}
sdk=${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}
ndk=${ANDROID_NDK_ROOT:-$(ls -d "$sdk"/ndk/* 2>/dev/null | sort -V | tail -n 1)}
if [ ! -f "$key" ] || [ ! -d "$sdk/build-tools" ] || [ ! -d "$sdk/platforms" ] || [ ! -d "$ndk" ]; then
    if [ -n "$optional" ]; then
        echo "APK step skipped (signing key or Android SDK/NDK missing); run later: sh scripts/apk-release.sh $tag"
        exit 0
    fi
    echo 'apk-release: provide the signing key (PITWALL_APK_KEY) and Android SDK/NDK' >&2
    exit 1
fi
key=$(CDPATH='' cd -- "$(dirname "$key")" && pwd)/$(basename "$key")

tmp=$(mktemp -d)
cleanup() {
    git -C "$root" worktree remove --force "$tmp/build" 2>/dev/null || true
    rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
git worktree add --quiet --detach "$tmp/build" "refs/tags/$tag"
cd "$tmp/build"
set -- $(sh scripts/apk.sh --version)
[ "$#" = 3 ] && [ "v$1" = "$tag" ] && [ "$3" = "$tag" ] ||
    { echo "apk-release: version does not match $tag: $*" >&2; exit 1; }

attempt=0
echo "Waiting for GitHub release $tag before building the APK"
until gh release view "$tag" >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 90 ] || { echo "apk-release: timed out waiting for $tag; run this command again later" >&2; exit 1; }
    sleep 20
done
PITWALL_APK_KEY=$key APK_LDFLAGS= ARCH=arm64 sh scripts/apk.sh
set -- $(sh scripts/apk.sh --version)
[ "$#" = 3 ] && [ "v$1" = "$tag" ] && [ "$3" = "$tag" ] ||
    { echo "apk-release: built version does not match $tag: $*" >&2; exit 1; }
gh release upload "$tag" dist/pitwall-phone.apk --clobber
