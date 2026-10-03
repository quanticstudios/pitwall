#!/bin/sh
# Installs a pitwall release on Linux or macOS; see "Install" in README.md.
set -eu

repo=quanticstudios/pitwall

# asset names the release archive for `uname -s` and `uname -m`.
asset() {
    case "$1" in
        Linux) os=linux ;;
        Darwin) os=darwin ;;
        *) printf 'pitwall: no release for %s; Windows uses scripts/get.ps1\n' "$1" >&2; return 1 ;;
    esac
    case "$2" in
        x86_64 | amd64) arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
        *) printf 'pitwall: no release for %s on %s\n' "$2" "$1" >&2; return 1 ;;
    esac
    printf 'pitwall_%s_%s.tar.gz\n' "$os" "$arch"
}

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d ' ' -f 1
    else
        shasum -a 256 "$1" | cut -d ' ' -f 1
    fi
}

main() {
    os=$(uname -s)
    arch=$(uname -m)
    # why: a shell under Rosetta reports x86_64 on an Apple silicon Mac.
    if [ "$os" = Darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
        arch=arm64
    fi
    name=$(asset "$os" "$arch")
    version=${PITWALL_VERSION:-}
    if [ -n "$version" ]; then
        base="https://github.com/$repo/releases/download/$version"
    else
        base="https://github.com/$repo/releases/latest/download"
    fi
    dir=${PITWALL_INSTALL_DIR:-"$HOME/.local/bin"}

    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    printf 'Downloading %s (%s)\n' "$name" "${version:-latest}"
    curl -fsSL -o "$tmp/$name" "$base/$name"
    curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
    want=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$tmp/checksums.txt")
    got=$(sha256 "$tmp/$name")
    if [ -z "$want" ] || [ "$want" != "$got" ]; then
        printf 'pitwall: %s does not match checksums.txt; nothing installed\n' "$name" >&2
        exit 1
    fi
    tar -xzf "$tmp/$name" -C "$tmp" pitwall
    mkdir -p "$dir"
    # why: a rename leaves a running pitwall on its old binary.
    cp "$tmp/pitwall" "$dir/.pitwall.new"
    chmod 755 "$dir/.pitwall.new"
    mv -f "$dir/.pitwall.new" "$dir/pitwall"
    if [ "$os" = Darwin ]; then
        xattr -d com.apple.quarantine "$dir/pitwall" 2>/dev/null || true
        printf '%s\n' 'Cleared the macOS quarantine flag: release binaries are not signed yet.'
    fi
    printf 'Installed %s in %s.\n' "$("$dir/pitwall" --version)" "$dir"
    case ":$PATH:" in
        *":$dir:"*) ;;
        *) printf 'Add %s to PATH in your shell profile.\n' "$dir" ;;
    esac
    printf '%s\n' 'Next: pitwall hooks install' 'Then run pitwall and trust Codex hooks once with /hooks.'
}

# why: main runs last, so a download cut short runs nothing.
if [ "${PITWALL_GET_TEST:-}" != 1 ]; then
    main
fi
