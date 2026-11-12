#!/bin/sh
set -eu

src=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/apk-release.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export GIT_CONFIG_GLOBAL="$tmp/gitconfig" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=dev GIT_AUTHOR_EMAIL=dev@example.com
export GIT_COMMITTER_NAME=dev GIT_COMMITTER_EMAIL=dev@example.com
: >"$GIT_CONFIG_GLOBAL"
mkdir -p "$tmp/repo/scripts" "$tmp/repo/dist/android" "$tmp/bin" "$tmp/sdk/build-tools" "$tmp/sdk/platforms" "$tmp/sdk/ndk/1"
export ANDROID_SDK_ROOT="$tmp/sdk" ANDROID_NDK_ROOT="$tmp/sdk/ndk/1"
unset PITWALL_APK_KEY APK_LDFLAGS
export APK_RELEASE_TEST="$tmp"
cat >"$tmp/bin/gh" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >>"$APK_RELEASE_TEST/gh.log"
if [ "$2" = view ] && [ ! -f "$APK_RELEASE_TEST/waited" ]; then
    touch "$APK_RELEASE_TEST/waited"
    exit 1
fi
if [ "$2" = upload ]; then
    [ "$3 $4 $5" = 'v0.3.0 dist/pitwall-phone.apk --clobber' ]
    [ "$(cat "$4")" = apk ]
fi
SH
cat >"$tmp/bin/sleep" <<'SH'
#!/bin/sh
echo waited >>"$APK_RELEASE_TEST/sleep.log"
SH
chmod +x "$tmp/bin/gh" "$tmp/bin/sleep"
export PATH="$tmp/bin:$PATH"
git init --quiet -b main "$tmp/repo"
cd "$tmp/repo"
printf 'dist/\n' >.gitignore
cp "$src" scripts/apk-release.sh
cat >scripts/apk.sh <<'SH'
#!/bin/sh
set -eu
if [ "${1:-}" = --version ]; then
    echo '0.3.0 300000 v0.3.0'
    exit
fi
[ "$(git status --porcelain)" = '' ]
[ "$(cat "$PITWALL_APK_KEY")" = key ]
[ "$ARCH" = arm64 ]
[ -z "$APK_LDFLAGS" ]
mkdir -p dist
echo apk >dist/pitwall-phone.apk
SH
git add .
git commit --quiet -m init
git tag v0.3.0
echo key >dist/android/debug.keystore
sh scripts/apk-release.sh v0.3.0 >"$tmp/output"
grep -qF 'release upload v0.3.0 dist/pitwall-phone.apk --clobber' "$tmp/gh.log"
[ "$(wc -l <"$tmp/sleep.log")" -eq 1 ]
[ "$(git worktree list --porcelain | grep -c '^worktree ')" -eq 1 ]

git worktree add --quiet -b other "$tmp/other"
(cd "$tmp/other" && sh scripts/apk-release.sh v0.3.0 >"$tmp/output")
git worktree remove "$tmp/other"

mv dist/android/debug.keystore "$tmp/override.key"
out=$(sh scripts/apk-release.sh --if-available v0.3.0)
printf '%s' "$out" | grep -qF 'APK step skipped'
PITWALL_APK_KEY="$tmp/override.key" sh scripts/apk-release.sh v0.3.0 >"$tmp/output"
out=$(PITWALL_APK_KEY="$tmp/override.key" ANDROID_SDK_ROOT="$tmp/missing-sdk" sh scripts/apk-release.sh --if-available v0.3.0)
printf '%s' "$out" | grep -qF 'APK step skipped'

cat >"$tmp/bin/gh" <<'SH'
#!/bin/sh
if [ "$2" = view ]; then exit 0; fi
echo 'upload-failure' >&2
exit 1
SH
if PITWALL_APK_KEY="$tmp/override.key" sh scripts/apk-release.sh v0.3.0 >"$tmp/output" 2>&1; then
    echo 'FAIL: succeeded after an upload failure' >&2
    exit 1
fi
grep -qF 'upload-failure' "$tmp/output"
[ "$(git worktree list --porcelain | grep -c '^worktree ')" -eq 1 ]

sed 's/v0.3.0/v0.3.0-1-gabc123/' scripts/apk.sh >scripts/apk.sh.tmp
mv scripts/apk.sh.tmp scripts/apk.sh
git add scripts/apk.sh
git commit --quiet -m mismatch
git tag v0.3.1
cp "$tmp/gh.log" "$tmp/before"
if PITWALL_APK_KEY="$tmp/override.key" sh scripts/apk-release.sh v0.3.1 >"$tmp/output" 2>&1; then
    echo 'FAIL: uploaded a mismatched version' >&2
    exit 1
fi
cmp "$tmp/before" "$tmp/gh.log"
[ "$(git worktree list --porcelain | grep -c '^worktree ')" -eq 1 ]

sed 's/0.3.0 300000 v0.3.0-1-gabc123/0.3.2 302000 v0.3.2/' scripts/apk.sh >scripts/apk.sh.tmp
mv scripts/apk.sh.tmp scripts/apk.sh
printf '\necho build-failure >&2\nexit 1\n' >>scripts/apk.sh
git add scripts/apk.sh
git commit --quiet -m 'build failure'
git tag v0.3.2
if PITWALL_APK_KEY="$tmp/override.key" sh scripts/apk-release.sh v0.3.2 >"$tmp/output" 2>&1; then
    echo 'FAIL: succeeded after a build failure' >&2
    exit 1
fi
grep -qF 'build-failure' "$tmp/output"
[ "$(git worktree list --porcelain | grep -c '^worktree ')" -eq 1 ]

cat >"$tmp/bin/gh" <<'SH'
#!/bin/sh
exit 1
SH
if PITWALL_APK_KEY="$tmp/override.key" sh scripts/apk-release.sh v0.3.0 >"$tmp/output" 2>&1; then
    echo 'FAIL: succeeded without a release' >&2
    exit 1
fi
grep -qF 'timed out' "$tmp/output"
[ "$(git worktree list --porcelain | grep -c '^worktree ')" -eq 1 ]
echo 'apk-release_test: ok'
