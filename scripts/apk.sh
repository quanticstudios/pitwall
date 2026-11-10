#!/bin/sh
# Builds cmd/pitwall-phone as a debug-signed Android APK and prints its path.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
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

ANDROID_HOME=$sdk ANDROID_SDK_ROOT=$sdk ANDROID_NDK_ROOT=$ndk GOFLAGS=-tags=novulkan \
    mise exec "$java" -- "$gogio" -target android -arch "$arch" -minsdk 28 -targetsdk 35 \
    -appid com.quanticstudios.pitwall -name pitwall -version "${VERSION:-0.0.0.1}" \
    -icon internal/remote/web/icon-512.png -signkey "$key" -signpass android \
    -o "$out" ./cmd/pitwall-phone
echo "$root/$out"
