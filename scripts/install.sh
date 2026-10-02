#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
prefix=${PREFIX:-"$HOME/.local"}
case "$prefix" in
    /*) ;;
    *) printf '%s\n' 'PREFIX must be an absolute path.' >&2; exit 1 ;;
esac

cd "$root"
mkdir -p "$prefix/bin" "$prefix/share/applications" "$prefix/share/icons/hicolor/scalable/apps"
GOFLAGS=-tags=novulkan mise exec -- go build -o "$prefix/bin/pitwall" ./cmd/pitwall

desktop_bin=$(printf '%s' "$prefix/bin/pitwall" | sed -e 's/[\\"`$]/\\&/g' -e 's/\\/\\\\/g' -e 's/%/%%/g')
while IFS= read -r line; do
    case "$line" in
        Exec=*) printf 'Exec="%s"\n' "$desktop_bin" ;;
        *) printf '%s\n' "$line" ;;
    esac
done < packaging/pitwall.desktop > "$prefix/share/applications/pitwall.desktop"
chmod 644 "$prefix/share/applications/pitwall.desktop"
install -m 644 packaging/pitwall.svg "$prefix/share/icons/hicolor/scalable/apps/pitwall.svg"

printf 'Installed pitwall in %s/bin.\n' "$prefix"
printf 'Add %s/bin to PATH if needed.\n' "$prefix"
printf '%s\n' 'Next: pitwall hooks install' 'Then run pitwall and trust Codex hooks once with /hooks.'
