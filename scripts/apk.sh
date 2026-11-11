#!/bin/sh
# Builds cmd/pitwall-phone as a debug-signed APK; --version prints "name code shown" instead.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

# why: Android updates only over a lower versionCode; vX.Y.Z plus N commits becomes XYYZZNNN, which every commit raises.
# warn: a shallow clone counts only the commits it has, which can lower the code.
[ "$(git rev-parse --is-shallow-repository)" = false ] ||
    { echo "apk.sh: a shallow clone counts too few commits; run git fetch --unshallow --tags" >&2; exit 1; }
# why: the highest stable tag, not the nearest: a merged maintenance tag (v0.2.10 into v0.3.x) would lower the code.
tag=$(git tag --merged HEAD --list 'v[0-9]*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1) || true
[ -n "$tag" ] || { echo "apk.sh: no vX.Y.Z tag in reach; run git fetch --tags" >&2; exit 1; }
# warn: git describe --match miscounts across merges; rev-list counts exactly.
n=$(git rev-list --count "refs/tags/$tag..HEAD")
describe=$tag-$n-g$(git rev-parse --short HEAD)
git diff --quiet HEAD -- || describe=$describe-dirty
v=${tag#v}
major=${v%%.*}
minor=${v#*.}
patch=${minor#*.}
minor=${minor%%.*}
case "$major.$minor.$patch.$n" in
*[!0-9.]* | .* | *..* | *.) echo "apk.sh: cannot read a version from $describe" >&2; exit 1 ;;
esac
[ "$major" -lt 200 ] && [ "$minor" -lt 100 ] && [ "$patch" -lt 100 ] && [ "$n" -lt 1000 ] ||
    { echo "apk.sh: $describe does not fit a versionCode: minor and patch under 100, at most 999 commits after the tag" >&2; exit 1; }
name=$major.$minor.$patch
code=$((((major * 100 + minor) * 100 + patch) * 1000 + n))
shown=${describe#v}
if [ "${1:-}" = --version ]; then
    echo "$name $code $shown"
    exit 0
fi

# ARCH: arm64 for phones, amd64 for the emulator, or both comma-separated.
arch=${ARCH:-arm64}
sdk=${ANDROID_SDK_ROOT:-$HOME/Android/Sdk}
ndk=${ANDROID_NDK_ROOT:-$(ls -d "$sdk"/ndk/* 2>/dev/null | sort -V | tail -n 1)}
java=${JAVA:-java@temurin-17}
out=dist/pitwall-phone.apk
# why: dist/ is ignored by git, so the debug keystore never gets committed.
key=dist/android/debug.keystore
gogio_version=$(mise exec -- go list -m -f '{{.Version}}' gioui.org)

[ -d "$sdk/build-tools" ] || { echo "apk.sh: no Android SDK at $sdk" >&2; exit 1; }
[ -d "$ndk" ] || { echo "apk.sh: no NDK under $sdk/ndk" >&2; exit 1; }
mkdir -p dist/android

gogio=$root/dist/android/gogio-$gogio_version/gogio
[ -x "$gogio" ] || GOFLAGS= GOBIN=$(dirname "$gogio") mise exec -- go install "gioui.org/cmd/gogio@$gogio_version"

if [ ! -f "$key" ]; then
    mise exec "$java" -- keytool -genkeypair -keystore "$key" -storepass android -keypass android \
        -alias androiddebugkey -dname "CN=Android Debug,O=Android,C=US" -keyalg RSA -keysize 2048 \
        -validity 10000 >/dev/null 2>&1
fi

# warn: gogio puts all four parts of -version in Android's versionName.
ANDROID_HOME=$sdk ANDROID_SDK_ROOT=$sdk ANDROID_NDK_ROOT=$ndk GOFLAGS=-tags=novulkan \
    mise exec "$java" -- "$gogio" -target android -arch "$arch" -minsdk 28 -targetsdk 35 \
    -appid com.quanticstudios.pitwall -name pitwall -version "$name.$code" \
    -ldflags "-X github.com/quanticstudios/pitwall/internal/ui/mobile.Version=$shown" \
    -icon internal/remote/web/icon-512.png -signkey "$key" -signpass android \
    -o "$out" ./cmd/pitwall-phone
echo "$root/$out"
