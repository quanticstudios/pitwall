#!/bin/sh
set -eu

optional=
if [ "${1:-}" = --if-available ]; then optional=1; shift; fi
[ "$#" = 1 ] || { echo 'usage: scripts/apk-release.sh [--if-available] vX.Y.Z' >&2; exit 2; }
tag=$1
if ! printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "APK step skipped for $tag; phone updates use stable vX.Y.Z releases."
    exit 0
fi
root=$(git rev-parse --show-toplevel)
tmp=
cleanup() {
    result=$?
    if [ -n "$tmp" ]; then
        git -C "$root" worktree remove --force "$tmp/build" 2>/dev/null || true
        rm -rf "$tmp"
    fi
    if [ "$result" -ne 0 ]; then echo "apk-release: retry: sh scripts/apk-release.sh $tag" >&2; fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
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
aapt=$(ls "$sdk"/build-tools/*/aapt 2>/dev/null | sort -V | tail -n 1)
[ -x "$aapt" ] || { echo 'apk-release: no aapt in Android build-tools' >&2; exit 1; }
permissions=$("$aapt" dump permissions dist/pitwall-phone.apk)
printf '%s\n' "$permissions" | grep -qF 'android.permission.REQUEST_INSTALL_PACKAGES' ||
    { echo 'apk-release: APK lacks REQUEST_INSTALL_PACKAGES' >&2; exit 1; }
manifest=$("$aapt" dump xmltree dist/pitwall-phone.apk AndroidManifest.xml)
printf '%s\n' "$manifest" | grep -qF 'com.quanticstudios.pitwall.ApkInstall$Result' ||
    { echo 'apk-release: APK lacks the installation result receiver' >&2; exit 1; }
set -- $(sh scripts/apk.sh --version)
[ "$#" = 3 ] && [ "v$1" = "$tag" ] && [ "$3" = "$tag" ] ||
    { echo "apk-release: built version does not match $tag: $*" >&2; exit 1; }
gh release upload "$tag" dist/pitwall-phone.apk --clobber
