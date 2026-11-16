#!/usr/bin/env python3
"""Drive pitwall over $PITWALL_SOCKET with the control commands.

    control.py [-s SESSION]                         list the tabs
    control.py [-s SESSION] read PANE               print a pane's screen
    control.py [-s SESSION] wait PANE UNTIL [MS]    wait until done, idle,
                                                    blocked or exit
    control.py [-s SESSION] new DIR [-- CMD...]     open a tab, print its
                                                    tab and pane ids
    control.py [-s SESSION] prompt PANE TEXT [--wait]
                                                    type a prompt; with
                                                    --wait print the reply

PANE is an address as pitwall read takes it: #1, 1.2, a title, or @ and a
pane id. Linux only; see docs/web-protocol.md, Control. Standard library.
"""

import argparse
import itertools
import json
import os
import socket
import sys


class Pitwall:
    def __init__(self, path):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.connect(path)
        self.file = self.sock.makefile("rwb")
        self.ids = itertools.count(1)
        self.send({"type": "hello", "protocol": 1, "client": {"name": "control.py"}})
        welcome = self.next()
        if welcome.get("type") != "welcome":
            sys.exit(f"control.py: no welcome: {welcome}")

    def send(self, frame):
        self.file.write(json.dumps(frame).encode() + b"\n")
        self.file.flush()

    def next(self):
        line = self.file.readline()
        if not line:
            sys.exit("control.py: the daemon closed the connection (a pitwall from before the JSON socket?)")
        return json.loads(line)

    def call(self, typ, **args):
        """Sends one command and returns its result's data; pushed frames
        that arrive first are skipped."""
        frame = {"type": typ, "id": str(next(self.ids))}
        frame.update({k: v for k, v in args.items() if v is not None})
        self.send(frame)
        while True:
            m = self.next()
            if m.get("type") == "result" and m.get("id") == frame["id"]:
                if not m["ok"]:
                    sys.exit(f"control.py: {m['code']}: {m['message']}")
                return m.get("data", {})


def main():
    p = argparse.ArgumentParser(description="Drive pitwall over $PITWALL_SOCKET.")
    p.add_argument("-s", dest="session", help="the session, while more than one is open")
    p.add_argument("--wait", action="store_true", help="prompt: wait for the reply")
    p.add_argument("command", nargs="?", choices=["read", "wait", "new", "prompt"])
    p.add_argument("args", nargs="*")
    a = p.parse_args()
    need = {None: (0, 0), "read": (1, 1), "wait": (2, 3), "new": (1, None), "prompt": (2, 2)}[a.command]
    if len(a.args) < need[0] or need[1] is not None and len(a.args) > need[1]:
        p.error(f"wrong arguments for {a.command}")
    path = os.environ.get("PITWALL_SOCKET")
    if not path:
        sys.exit("control.py: run it in a pitwall pane, or set PITWALL_SOCKET")
    pw = Pitwall(path)
    if a.command is None:
        data = pw.call("listTabs", session=a.session)
        for t in data["tabs"]:
            print(f"{t['addr']}\t{t['state'] or '-'}\t{t['title']}")
    elif a.command == "read":
        print(pw.call("readPane", pane=a.args[0], session=a.session, screen=True)["text"])
    elif a.command == "wait":
        until = a.args[1]
        if until not in ("done", "idle", "blocked", "exit"):
            p.error("UNTIL is done, idle, blocked or exit")
        ms = int(a.args[2]) if len(a.args) > 2 else None
        data = pw.call("waitPane", pane=a.args[0], session=a.session, until=until, timeoutMs=ms)
        print(json.dumps(data, separators=(",", ":")))
    elif a.command == "new":
        # A retry with the same startId never opens a second tab.
        start = os.urandom(8).hex()
        data = pw.call("newTab", dir=os.path.abspath(a.args[0]), cmd=a.args[1:] or None, session=a.session, startId=start)
        print(f"{data['tab']}\t{data.get('pane', '')}")
    else:
        data = pw.call("promptPane", pane=a.args[0], text=a.args[1], session=a.session, wait=a.wait or None)
        if a.wait:
            print(data.get("reply") or f"{data['outcome']}: {data.get('question', '')}")


if __name__ == "__main__":
    main()
