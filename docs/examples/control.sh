#!/usr/bin/env bash
# Drive pitwall over $PITWALL_SOCKET with the control commands, as
# control.py does, with socat and jq.
#
#   control.sh [-s SESSION]                         list the tabs
#   control.sh [-s SESSION] read PANE               print a pane's screen
#   control.sh [-s SESSION] wait PANE UNTIL [MS]    wait until done, idle,
#                                                   blocked or exit
#
# Linux only; see docs/web-protocol.md, Control.
set -euo pipefail

sock=${PITWALL_SOCKET:?run it in a pitwall pane, or set PITWALL_SOCKET}
session=
if [ "${1-}" = -s ]; then
	session=$2
	shift 2
fi

# One connection for the whole script: a waitPane's result comes only
# while the script's end stays open.
coproc PW { socat - UNIX-CONNECT:"$sock"; }
exec 3<&"${PW[0]}" 4>&"${PW[1]}" # a coproc's own descriptors close in subshells

send() { printf '%s\n' "$1" >&4; }
next() {
	local line
	IFS= read -r line <&3 || { echo "control.sh: the daemon closed the connection" >&2; exit 1; }
	printf '%s\n' "$line"
}

# call sends a command and prints its result's data, skipping pushed frames.
call() {
	local frame=$1 id line
	id=$(jq -r .id <<<"$frame")
	send "$frame"
	while line=$(next); do
		if jq -e --arg id "$id" '.type == "result" and .id == $id' <<<"$line" >/dev/null; then
			if jq -e .ok <<<"$line" >/dev/null; then
				jq -c '.data // {}' <<<"$line"
				return
			fi
			jq -r '"control.sh: \(.code): \(.message)"' <<<"$line" >&2
			exit 1
		fi
	done
}

send '{"type":"hello","protocol":1,"client":{"name":"control.sh"}}'
next | jq -e '.type == "welcome"' >/dev/null

case ${1-} in
"")
	call "$(jq -nc --arg s "$session" '{type: "listTabs", id: "1"} + if $s == "" then {} else {session: $s} end')" |
		jq -r '.tabs[] | "\(.addr)\t\(if .state == "" then "-" else .state end)\t\(.title)"'
	;;
read)
	call "$(jq -nc --arg s "$session" --arg p "$2" '{type: "readPane", id: "1", pane: $p, screen: true} + if $s == "" then {} else {session: $s} end')" |
		jq -r .text
	;;
wait)
	call "$(jq -nc --arg s "$session" --arg p "$2" --arg u "$3" --arg ms "${4-0}" \
		'{type: "waitPane", id: "1", pane: $p, until: $u, timeoutMs: ($ms | tonumber)} + if $s == "" then {} else {session: $s} end')"
	;;
*)
	echo "usage: control.sh [-s session] [read PANE | wait PANE UNTIL [MS]]" >&2
	exit 2
	;;
esac
