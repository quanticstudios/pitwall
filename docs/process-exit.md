# Pane exits after handoff

`pane.Start` still waits through `exec.Cmd.Wait` on Unix and through the
process handle on Windows. This reaps children and preserves today's exit
codes. `pane.Adopt` consumes a transferred PTY master and watches an existing
PID through a small internal `exitWaiter` interface. It starts the same reader
and bounded output drain as `Start`; a descendant keeping the PTY open cannot
hide the watched process's exit.

Linux uses `pidfd_open` and `poll`. A pidfd tracks the process even if its PID
is reused. After exit, `waitid` with `WNOWAIT` checks whether the process is
ours to reap; `wait4` then collects its status. For non-children, recent
kernels expose the status through `PIDFD_GET_INFO`. Older kernels provide
only the exit event, so `ExitCode` returns `-1`, the existing unknown/signal
sentinel. PTY EOF carries no numeric status. A watcher failure falls back to
PTY EOF and also reports `-1`.

Pidfd metadata appears after reaping. If needed, the watcher waits up to the
output drain timeout for the pidfd's hangup event before reading metadata
again. A parent that postpones reaping cannot hold the pane open forever.

We chose pidfds over a long-lived parent with `PR_SET_CHILD_SUBREAPER`.
Pidfds observe children already started by the old daemon and avoid adding
another process and a status protocol. A subreaper can only adopt descendants;
setting it in the replacement daemon cannot claim the old daemon's children.
Orphans are reaped by init or the existing ancestor subreaper. If reparenting
does make the replacement their parent, the watcher reaps them itself.

macOS registers `EVFILT_PROC` with `NOTE_EXIT | NOTE_EXITSTATUS` on a kqueue.
Current XNU permits status collection for a process the watcher may signal,
including another process's child running under the same user. Register
before the old daemon releases the child: kqueue proc events cover future
exits only. A registration failure rejects adoption so the caller can fall
back to restart. Windows rejects adoption and keeps its existing restart path.

The integration tests start a child in a helper process, adopt its PTY,
exit the helper, and check output, status, signals, close, and the absence of
zombies. They also keep the helper alive for one case to prove that monitoring
does not require parentage, and keep a descendant's PTY open for another.
This change provides the pane adoption entry point; transporting descriptors,
restoring emulator snapshots, and switching daemons belong to the handoff work.

Sources: [Linux pidfd semantics](https://man7.org/linux/man-pages/man2/pidfd_open.2.html),
[Linux pidfd exit metadata](https://github.com/torvalds/linux/blob/master/fs/pidfs.c),
[XNU proc filter and status permissions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_event.c).
