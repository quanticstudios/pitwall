#!/bin/sh
# Cuts a release from main in two runs; see "To cut a release" in CHANGELOG.md.
set -eu

usage='usage: scripts/release.sh [--tag] [--dry-run] [version]'
dry=
tag=
version=
for arg in "$@"; do
    case "$arg" in
        --dry-run) dry=1 ;;
        --tag) tag=1 ;;
        -*) printf '%s\n' "$usage" >&2; exit 2 ;;
        *) version=$arg ;;
    esac
done

die() {
    printf 'release: %s\n' "$*" >&2
    exit 1
}
step() { printf '==> %s\n' "$*"; }
# run prints a command that changes something and runs it unless --dry-run.
run() {
    printf '+ %s\n' "$*"
    if [ -z "$dry" ]; then
        "$@"
    fi
}

root=$(git rev-parse --show-toplevel) || die 'not in a git repository'
cd "$root"

step 'Checking for a clean, up-to-date main'
branch=$(git symbolic-ref --short -q HEAD || true)
[ "$branch" = main ] || die "on ${branch:-a detached HEAD}, not main; run: git switch main"
# why: untracked files never reach the release commit, which stages CHANGELOG.md alone.
if ! git diff --quiet || ! git diff --cached --quiet; then
    die 'main has uncommitted changes; commit or stash them first'
fi
git fetch --quiet --tags origin
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] ||
    die 'main differs from origin/main; run: git pull --ff-only'

if [ -z "$version" ]; then
    # The newest pre-release series: v0.1.0-beta.* sorts after v0.1.0-alpha.*.
    latest=$(git tag --list 'v0.1.0-*' --sort=-v:refname | head -n 1)
    n=${latest##*.}
    case "$n" in
        '' | *[!0-9]*) n=0 ;;
    esac
    series=${latest%.*}
    version=${series:-v0.1.0-beta}.$((n + 1))
    step "Next version after ${latest:-no tags}: $version"
else
    step "Version: $version"
fi
case "$version" in
    v[0-9]*) ;;
    *) die "version $version does not start with v and a digit" ;;
esac
case "$version" in
    *[!A-Za-z0-9.+-]*) die "version $version has characters a tag should not" ;;
esac
# why: the fetch above brought every tag on origin, and a pushed tag can never move.
git rev-parse -q --verify "refs/tags/$version" >/dev/null &&
    die "tag $version already exists; pass the next version"

if [ -n "$tag" ]; then
    step "Finding the commit on origin/main that added '## $version'"
    re=$(printf '%s' "$version" | sed 's/[.+]/\\&/g')
    commit=$(git log --format=%H -G "^## $re\$" origin/main -- CHANGELOG.md | tail -n 1)
    if [ -z "$commit" ] || ! git show "$commit:CHANGELOG.md" | grep -qxF "## $version"; then
        die "no commit on main adds '## $version' to CHANGELOG.md; merge the release PR (gh pr view release-$version), run git pull --ff-only, then run this again"
    fi
    git log -1 --format='    %h %s' "$commit"
    step "Tagging $commit and pushing the tag (once: release tags are immutable)"
    run git tag -a "$version" -m "pitwall $version" "$commit"
    run git push origin "refs/tags/$version"
    step "Done. Watch the release build: gh run list --workflow release.yml"
    exit 0
fi

step "Renaming '## Unreleased' to '## $version' in CHANGELOG.md"
top=$(grep -m 1 '^## ' CHANGELOG.md || true)
[ "$top" = '## Unreleased' ] ||
    die "the top CHANGELOG.md section is '${top:-none}', not '## Unreleased'; add the release notes under '## Unreleased' first"
awk '/^## /{n++; next} n==1' CHANGELOG.md | grep -q '[^[:space:]]' ||
    die "'## Unreleased' in CHANGELOG.md is empty; add the release notes first"
grep -qxF "## $version" CHANGELOG.md && die "CHANGELOG.md already has '## $version'"

pr=release-$version
run git switch --quiet -c "$pr"
if [ -z "$dry" ]; then
    awk -v v="## $version" '!done && $0 == "## Unreleased" {print v; done=1; next} {print}' \
        CHANGELOG.md >CHANGELOG.md.tmp
    mv CHANGELOG.md.tmp CHANGELOG.md
fi
run git commit --quiet -m "Release $version" -- CHANGELOG.md
run git push --quiet origin "$pr"
run gh pr create --base main --head "$pr" --title "Release $version" --body "Renames \`## Unreleased\` to \`## $version\` in CHANGELOG.md. After this merges, \`sh scripts/release.sh --tag $version\` on an up-to-date main tags the merge commit and pushes the tag."
run git switch --quiet main
step "Next: merge the PR once CI passes, run git pull --ff-only, then: sh scripts/release.sh --tag $version"
