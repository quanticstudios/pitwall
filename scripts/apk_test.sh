#!/bin/sh
# Checks apk.sh's version stamp against a temp repo's tags: sh scripts/apk_test.sh
set -eu

src=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/apk.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# why: the developer's global git config and hooks must not change the result.
export GIT_CONFIG_GLOBAL="$tmp/gitconfig" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=dev GIT_AUTHOR_EMAIL=dev@example.com
export GIT_COMMITTER_NAME=dev GIT_COMMITTER_EMAIL=dev@example.com
: >"$GIT_CONFIG_GLOBAL"

fail=0
bad() {
    printf 'FAIL: %s\n' "$*" >&2
    fail=1
}

git init --quiet -b main "$tmp/repo"
cd "$tmp/repo"
mkdir scripts
cp "$src" scripts/apk.sh
printf 'dist/\n' >.gitignore
git add .
git commit --quiet -m init

ver() { sh scripts/apk.sh --version; }
commit() { git commit --quiet --allow-empty -m "$1"; }

if out=$(ver 2>&1); then
    bad "no tag: printed $out, want a refusal"
fi

git tag -a v0.2.9 -m v0.2.9
[ "$(ver)" = "0.2.9 209000 v0.2.9" ] || bad "on v0.2.9: $(ver)"
last=209000

commit a
commit b
set -- $(ver)
[ "$1 $2" = "0.2.9 209002" ] || bad "two commits after v0.2.9: $*"
[ "$2" -gt "$last" ] || bad "a commit after the tag did not raise the code: $2 after $last"
last=$2

git tag -a v0.3.0-beta.1 -m beta
set -- $(ver)
[ "$2" = 209002 ] || bad "a beta tag changed the code: $*"

commit c
git tag -a v0.2.10 -m v0.2.10
set -- $(ver)
[ "$1 $2" = "0.2.10 210000" ] || bad "on v0.2.10: $*"
[ "$2" -gt "$last" ] || bad "the next release did not raise the code: $2 after $last"
last=$2

commit d
git tag -a v1.0.0 -m v1.0.0
set -- $(ver)
[ "$2" = 10000000 ] && [ "$2" -gt "$last" ] || bad "on v1.0.0: $* after $last"

echo dirty >>.gitignore
set -- $(ver)
[ "$2" = 10000000 ] && [ "${3%-dirty}" != "$3" ] || bad "a dirty tree: $*"
git checkout --quiet .gitignore
last=10000000

# A maintenance release merged later is nearer than v1.0.0 but lower.
git checkout --quiet -b maint v0.2.10
commit m
git tag -a v0.2.11 -m v0.2.11
git checkout --quiet main
commit e1
commit e2
git merge --quiet --no-ff -m "merge maint" maint
set -- $(ver)
[ "$1 $2" = "1.0.0 10000004" ] && [ "$2" -gt "$last" ] || bad "after merging v0.2.11 into v1.0.0: $* after $last"

# A shallow clone can reach v1.0.0 and still miss commits after it.
git clone --quiet --depth 4 "file://$tmp/repo" "$tmp/shallow"
if out=$(cd "$tmp/shallow" && sh scripts/apk.sh --version 2>&1); then
    bad "a shallow clone: printed $out, want a refusal"
fi
case $out in *--unshallow*) ;; *) bad "a shallow clone's refusal does not say to fetch the history: $out" ;; esac

commit e
git tag -a v1.100.0 -m v1.100.0
if out=$(ver 2>&1); then
    bad "minor 100 fits no code: printed $out, want a refusal"
fi

[ "$fail" = 0 ] && echo "apk_test: ok"
exit "$fail"
