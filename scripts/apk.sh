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
describe=$tag
[ "$n" = 0 ] || describe=$tag-$n-g$(git rev-parse --short HEAD)
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
shown=$describe
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
key=${PITWALL_APK_KEY:-dist/android/debug.keystore}
gogio_version=$(mise exec -- go list -m -f '{{.Version}}' gioui.org)

[ -d "$sdk/build-tools" ] || { echo "apk.sh: no Android SDK at $sdk" >&2; exit 1; }
[ -d "$ndk" ] || { echo "apk.sh: no NDK under $sdk/ndk" >&2; exit 1; }
mkdir -p dist/android

gogio=$root/dist/android/gogio-$gogio_version-installer/gogio
if [ ! -x "$gogio" ]; then
    GOFLAGS= mise exec -- go mod download "gioui.org/cmd@$gogio_version"
    src=$(mise exec -- go env GOMODCACHE)/gioui.org/cmd@$gogio_version
    build=$(mktemp -d)
    trap 'rm -rf "$build"' EXIT
    cp -R "$src/." "$build/"
    chmod -R u+w "$build"
    # why: gogio includes versionCode in versionName; the release name must match the tag.
    grep -qF 'android:versionName="{{.Version}}"' "$build/gogio/androidbuild.go" ||
        { echo 'apk.sh: gogio versionName template changed; check the local patch' >&2; exit 1; }
    sed -e 's/android:versionName="{{.Version}}"/android:versionName="v{{.Version.StringCompact}}"/' \
        -e 's#<application {{.IconSnip}}#<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES"/><application {{.IconSnip}}#' \
        -e 's#</application>#<receiver android:name="com.quanticstudios.pitwall.ApkInstall$Result" android:exported="false"/></application>#' \
        "$build/gogio/androidbuild.go" >"$build/gogio/androidbuild.go.tmp"
    mv "$build/gogio/androidbuild.go.tmp" "$build/gogio/androidbuild.go"
    GOFLAGS= mise exec -- go -C "$build" build -o "$gogio" ./gogio
    rm -rf "$build"
    trap - EXIT
fi

if [ ! -f "$key" ]; then
    [ -z "${PITWALL_APK_KEY:-}" ] || { echo "apk.sh: no signing key at $key" >&2; exit 1; }
    mise exec "$java" -- keytool -genkeypair -keystore "$key" -storepass android -keypass android \
        -alias androiddebugkey -dname "CN=Android Debug,O=Android,C=US" -keyalg RSA -keysize 2048 \
        -validity 10000 >/dev/null 2>&1
fi

# gogio includes jars from Go package folders; compile the phone's Java here.
platform=$(ls -d "$sdk"/platforms/android-* | sort -V | tail -n 1)
classes=$(mktemp -d)
jar=internal/ui/mobile/phone-platform.jar
trap 'rm -rf "$classes" "$jar"' EXIT
mise exec "$java" -- javac -source 1.8 -target 1.8 -Xlint:-options -bootclasspath "$platform/android.jar" \
    -d "$classes" internal/ui/mobile/*.java
mise exec "$java" -- jar cf "$jar" -C "$classes" .

# APK_LDFLAGS can override Version, releaseURL and apkTestURL for local emulator checks.
ANDROID_HOME=$sdk ANDROID_SDK_ROOT=$sdk ANDROID_NDK_ROOT=$ndk GOFLAGS=-tags=novulkan \
    mise exec "$java" -- "$gogio" -target android -arch "$arch" -minsdk 28 -targetsdk 35 \
    -appid com.quanticstudios.pitwall -name pitwall -version "$name.$code" \
    -ldflags "-X github.com/quanticstudios/pitwall/internal/ui/mobile.Version=$shown ${APK_LDFLAGS:-}" \
    -icon internal/remote/web/icon-512.png -signkey "$key" -signpass android \
    -o "$out" ./cmd/pitwall-phone
echo "$root/$out"
