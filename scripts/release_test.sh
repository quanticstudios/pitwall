#!/bin/sh
# Runs release.sh against a temp repo with a local origin and a stub gh: sh scripts/release_test.sh
set -eu

script=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)/release.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# why: the developer's global git config and hooks must not change the result.
export GIT_CONFIG_GLOBAL="$tmp/gitconfig" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=dev GIT_AUTHOR_EMAIL=dev@example.com
export GIT_COMMITTER_NAME=dev GIT_COMMITTER_EMAIL=dev@example.com
: >"$GIT_CONFIG_GLOBAL"

mkdir "$tmp/bin"
cat >"$tmp/bin/gh" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>"$tmp/gh.log"
echo https://github.com/example/pitwall/pull/1
EOF
chmod +x "$tmp/bin/gh"
PATH="$tmp/bin:$PATH"

fail=0
bad() {
    printf 'FAIL: %s\n' "$*" >&2
    fail=1
}

# setup makes a fresh origin and clone whose CHANGELOG.md top is $1.
setup() {
    rm -rf "$tmp/origin.git" "$tmp/repo" "$tmp/gh.log"
    git init --quiet --bare -b main "$tmp/origin.git"
    git init --quiet -b main "$tmp/repo"
    cd "$tmp/repo"
    git remote add origin "$tmp/origin.git"
    printf '# Changelog\n\n%s\n## v0.1.0-alpha.10\n\n- Old.\n' "$1" >CHANGELOG.md
    git add CHANGELOG.md
    git commit --quiet -m init
    for n in 2 9 10; do
        git tag -a "v0.1.0-alpha.$n" -m "v0.1.0-alpha.$n"
    done
    git push --quiet origin main --tags
}

# refuses runs release.sh with $2.. and expects a failure whose message has $1.
refuses() {
    want=$1
    shift
    if out=$(sh "$script" "$@" 2>&1); then
        bad "release.sh $* succeeded, want a refusal mentioning '$want'"
    elif ! printf '%s' "$out" | grep -qF "$want"; then
        bad "release.sh $* said: $out; want '$want'"
    fi
}

unreleased='## Unreleased

- New thing.
'

setup "$unreleased"
before=$(git rev-parse HEAD)
out=$(sh "$script" --dry-run)
printf '%s' "$out" | grep -qF 'v0.1.0-alpha.11' || bad "dry run picked no v0.1.0-alpha.11: $out"
[ "$(git rev-parse HEAD)" = "$before" ] || bad 'dry run moved HEAD'
git diff --quiet || bad 'dry run changed CHANGELOG.md'
git rev-parse -q --verify refs/heads/release-v0.1.0-alpha.11 >/dev/null && bad 'dry run made a branch'
[ -z "$(git ls-remote --heads origin release-v0.1.0-alpha.11)" ] || bad 'dry run pushed'
[ ! -e "$tmp/gh.log" ] || bad 'dry run called gh'

sh "$script" >/dev/null
[ "$(git symbolic-ref --short HEAD)" = main ] || bad 'release did not switch back to main'
pr=$(git rev-parse origin/release-v0.1.0-alpha.11) || bad 'release pushed no branch'
[ "$(git log -1 --format=%s "$pr")" = 'Release v0.1.0-alpha.11' ] || bad 'wrong release commit subject'
git show "$pr:CHANGELOG.md" | grep -qx '## v0.1.0-alpha.11' || bad 'Unreleased was not renamed'
git show "$pr:CHANGELOG.md" | grep -q '^## Unreleased' && bad 'Unreleased is still there'
grep -qF -- '--head release-v0.1.0-alpha.11' "$tmp/gh.log" || bad 'gh pr create was not called'

refuses 'merge the release PR' --tag v0.1.0-alpha.11
[ -z "$(git ls-remote --tags origin v0.1.0-alpha.11)" ] || bad '--tag pushed a tag before the merge'

# why: a squash merge lands the same change as a new commit on main.
git cherry-pick "$pr" >/dev/null
git commit --quiet --amend -m 'Release v0.1.0-alpha.11 (#1)'
merged=$(git rev-parse HEAD)
printf 'x\n' >feature.txt
git add feature.txt
git commit --quiet -m 'A later feature'
git push --quiet origin main
sh "$script" --tag --dry-run >/dev/null
[ -z "$(git ls-remote --tags origin v0.1.0-alpha.11)" ] || bad '--tag --dry-run pushed a tag'
sh "$script" --tag >/dev/null 2>&1
got=$(git ls-remote origin 'refs/tags/v0.1.0-alpha.11^{}' | cut -f 1)
[ "$got" = "$merged" ] || bad "tag points at $got, want the release commit $merged"
refuses 'already exists' --tag v0.1.0-alpha.11

setup "$unreleased"
out=$(sh "$script" --dry-run v0.2.0)
printf '%s' "$out" | grep -qF 'release-v0.2.0' || bad "explicit version ignored: $out"
refuses 'already exists' v0.1.0-alpha.10
printf 'dirty\n' >>CHANGELOG.md
refuses 'uncommitted changes'
git checkout -- CHANGELOG.md
git switch --quiet -c other
refuses 'not main'

setup ''
refuses "not '## Unreleased'"

setup '## Unreleased
'
refuses 'is empty'

exit "$fail"
