#!/bin/sh
set -eu
file=${1:?usage: apk-gogio.sh androidbuild.go}
tmp=$file.tmp
trap 'rm -f "$tmp"' EXIT
grep -qF 'android:versionName="{{.Version}}"' "$file" ||
    { echo 'apk.sh: gogio versionName template changed' >&2; exit 1; }
sed -e 's/android:versionName="{{.Version}}"/android:versionName="v{{.Version.StringCompact}}"/' \
    -e 's#<application {{.IconSnip}}#<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES"/><application {{.IconSnip}}#' \
    -e 's#</application>#<receiver android:name="com.quanticstudios.pitwall.ApkInstall$Result" android:exported="false"/></application>#' \
    "$file" >"$tmp"
for entry in 'android:versionName="v{{.Version.StringCompact}}"' \
    'android:name="android.permission.REQUEST_INSTALL_PACKAGES"' \
    'android:name="com.quanticstudios.pitwall.ApkInstall$Result"'; do
    [ "$(grep -cF "$entry" "$tmp" || true)" -eq 1 ] ||
        { echo "apk.sh: gogio manifest patch must insert exactly one $entry" >&2; exit 1; }
done
mv "$tmp" "$file"
