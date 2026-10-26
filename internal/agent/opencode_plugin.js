// pitwall's OpenCode plugin, from `pitwall hooks install`; edited copies are left alone.
import { spawn } from "node:child_process";

const bin = __PITWALL_BIN__;

// why: pitwall redacts a reply whole before it cuts it, so past the cap it ends at whitespace, never mid-token.
function text(s) {
	const cps = Array.from(String(s ?? "").trim());
	if (cps.length <= 16000) return cps.join("");
	const cut = cps.slice(0, 16000).join("");
	const i = cut.search(/\s\S*$/);
	return i > 0 ? cut.slice(0, i) : "";
}

// tail is the end of a failed tool's output, where its error is.
function tail(s) {
	const cps = Array.from(String(s ?? "").trim());
	return cps.slice(-16000).join("");
}

// check runs `pitwall guard` on a shell command in a guarded tab and resolves
// to the reason it denies it, or "" when it allows it or cannot run.
function check(command) {
	return new Promise((resolve) => {
		let reason = "";
		try {
			const child = spawn(bin, ["guard"], { stdio: ["pipe", "ignore", "pipe"], windowsHide: true });
			const timer = setTimeout(() => {
				try {
					child.kill("SIGKILL");
				} catch {}
				resolve("");
			}, 5000);
			child.stderr?.on("data", (d) => (reason += d));
			child.on("error", () => {
				clearTimeout(timer);
				resolve("");
			});
			child.on("close", (code) => {
				clearTimeout(timer);
				resolve(code === 2 ? reason.trim() || "pitwall guard: denied" : "");
			});
			child.stdin?.on("error", () => {});
			child.stdin?.end(JSON.stringify({ tool_input: { command } }));
		} catch {
			resolve("");
		}
	});
}

// reporter returns send, which reports one event in Claude Code's hook names.
function reporter() {
	// why: one `pitwall hook opencode` at a time keeps events in order.
	const limit = 32;
	const pending = [];
	let busy = false;

	function run(payload) {
		return new Promise((resolve) => {
			let timer;
			let settled = false;
			const done = () => {
				if (settled) return;
				settled = true;
				clearTimeout(timer);
				resolve();
			};
			try {
				const child = spawn(bin, ["hook", "opencode"], { stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
				// warn: a child may ignore SIGTERM, so the deadline kills it and moves on regardless.
				timer = setTimeout(() => {
					try {
						child.kill("SIGKILL");
					} catch {}
					done();
				}, 5000);
				// why: a report never keeps OpenCode from exiting.
				child.unref();
				child.stdin?.unref?.();
				timer.unref?.();
				child.on("error", done);
				child.on("close", done);
				child.stdin?.on("error", () => {});
				child.stdin?.end(JSON.stringify(payload));
			} catch {
				done();
			}
		});
	}

	function pump() {
		if (busy) return;
		const payload = pending.shift();
		if (!payload) return;
		busy = true;
		run(payload).then(() => {
			busy = false;
			pump();
		});
	}

	return function send(event, session, fields = {}) {
		pending.push({ hook_event_name: event, session_id: session ?? "", ...fields });
		if (pending.length > limit) pending.shift();
		pump();
	};
}

// server is OpenCode 1's plugin, in its TUI's process: it reports to the pane it runs in, and outside one does nothing.
async function server() {
	if (!process.env.PITWALL_PANE) return {};
	const send = reporter();
	const children = new Set(); // subagent sessions, which never set the pane's state
	const replies = new Set(); // ids of assistant messages
	const last = new Map(); // session: the text of its latest reply
	const status = new Map(); // session: "busy" or "idle"
	const ended = new Set(); // sessions whose run failed or was aborted: their idle is no completion
	const plans = new Set(); // call ids of plan_exit, whose question asks to approve the plan
	const failed = new Set(); // call ids of tool calls reported as failed

	// fail reports a main session's tool call that errored, or a shell command
	// that exited non-zero, once.
	function fail(part) {
		const st = part.state ?? {};
		const exit = st.metadata?.exit;
		const nonzero = st.status === "completed" && typeof exit === "number" && exit !== 0;
		if (!main(part.sessionID) || failed.has(part.callID) || (st.status !== "error" && !nonzero)) return;
		failed.add(part.callID);
		if (failed.size > 256) failed.delete(failed.values().next().value);
		const input = st.input ?? {};
		send("PostToolUseFailure", part.sessionID, {
			tool_name: String(part.tool ?? ""),
			tool_input: { command: input.command, file_path: input.filePath },
			error: tail(st.status === "error" ? st.error : (st.output ?? st.metadata?.output)),
			exit_code: typeof exit === "number" ? exit : undefined,
			is_interrupt: st.metadata?.interrupted === true,
			duration_ms: st.time?.start && st.time?.end ? st.time.end - st.time.start : undefined,
		});
	}

	function main(session) {
		return typeof session === "string" && session !== "" && !children.has(session);
	}

	function settle(session) {
		if (status.get(session) !== "busy") return;
		status.set(session, "idle");
		if (ended.delete(session)) return;
		send("Stop", session, { last_assistant_message: text(last.get(session)) });
	}

	// why: an OpenCode idle at its first prompt has reported, so pitwall knows the plugin is in.
	send("SessionStart");

	return {
		"chat.message": async (input, output) => {
			try {
				if (!main(input?.sessionID)) return;
				const parts = Array.isArray(output?.parts) ? output.parts : [];
				const prompt = parts.filter((p) => p && p.type === "text" && !p.synthetic).map((p) => String(p.text)).join("\n");
				ended.delete(input.sessionID);
				last.delete(input.sessionID);
				status.set(input.sessionID, "busy");
				send("UserPromptSubmit", input.sessionID, { prompt });
			} catch {}
		},
		"tool.execute.before": async (input, output) => {
			try {
				if (main(input?.sessionID)) {
					if (input.tool === "plan_exit" && input.callID) plans.add(input.callID);
					send("PreToolUse", input.sessionID, { tool_name: String(input.tool ?? "") });
				}
			} catch {}
			const command = output?.args?.command;
			// why: an error thrown here stops the tool call, subagents' included, and the model reads its message.
			if (process.env.PITWALL_GUARD && typeof command === "string") {
				const reason = await check(command);
				if (reason) throw new Error(reason);
			}
		},
		event: async ({ event }) => {
			try {
				const p = event?.properties ?? {};
				switch (event?.type) {
					case "session.created":
					case "session.updated":
						if (p.info?.parentID) children.add(p.info.id);
						return;
					case "message.updated":
						if (p.info?.role === "assistant") replies.add(p.info.id);
						return;
					case "message.part.updated":
						if (p.part?.type === "text" && !p.part.synthetic && replies.has(p.part.messageID)) last.set(p.part.sessionID, p.part.text);
						if (p.part?.type === "tool") fail(p.part);
						return;
				}
				const session = p.sessionID;
				if (!main(session)) return;
				switch (event.type) {
					case "session.status":
						if (p.status?.type === "idle") settle(session);
						else if (p.status?.type === "busy" && status.get(session) !== "busy") {
							status.set(session, "busy");
							ended.delete(session);
							send("PostToolUse", session);
						}
						return;
					case "session.idle":
						settle(session);
						return;
					case "session.error":
						ended.add(session);
						if (p.error?.name === "MessageAbortedError") send("Interrupt", session);
						else send("StopFailure", session, { error: text(p.error?.data?.message || p.error?.name || "error") });
						return;
					case "permission.asked":
						send("Notification", session, { notification_type: "permission_prompt", message: String(p.permission ?? "") });
						return;
					case "question.asked":
						// why: plan_exit asks whether to switch to the build agent; pitwall shows that as a plan to approve.
						if (plans.delete(p.tool?.callID)) {
							send("PreToolUse", session, { tool_name: "ExitPlanMode" });
							return;
						}
						send("Notification", session, { notification_type: "elicitation_dialog", message: String(p.questions?.[0]?.question || "question") });
						return;
					case "permission.replied":
					case "question.replied":
					case "question.rejected":
						send("PostToolUse", session);
						return;
				}
			} catch {}
		},
	};
}

// tui is OpenCode 2's plugin, from plugins/pitwall/tui.js in each TUI: it reports the sessions that TUI shows to its pane.
function tui(ctx) {
	// why: OpenCode 2 also runs this from plugins/pitwall.js in its background service, which every TUI shares, so it cannot tell their panes apart there.
	if (!process.env.PITWALL_PANE || !ctx?.ui || !ctx.data?.listen) return;
	const send = reporter();
	const children = new Set(); // subagent sessions, which never set the pane's state
	const shown = new Set(); // sessions this TUI has shown, whose events are this pane's
	const busy = new Set(); // sessions with a run going
	const last = new Map(); // session: the text of its latest reply
	const calls = new Map(); // tool call id: its tool, input and start

	// main reports whether session is a main session this TUI has open, in its route or a tab.
	function main(session) {
		if (typeof session !== "string" || session === "" || children.has(session)) return false;
		if (shown.has(session)) return true;
		const route = ctx.ui.router.current();
		if ((route?.type !== "session" || route.sessionID !== session) && !ctx.ui.tabs.list().some((t) => t.sessionID === session)) return false;
		shown.add(session);
		return true;
	}

	// fail reports a tool call that failed, or a shell command that exited non-zero.
	function fail(session, d, created, error, exit) {
		const call = calls.get(d.id) ?? {};
		const input = call.input ?? {};
		send("PostToolUseFailure", session, {
			tool_name: String(call.tool ?? ""),
			tool_input: { command: input.command, file_path: input.filePath ?? input.path },
			error: tail(error),
			exit_code: exit,
			duration_ms: typeof created === "number" && typeof call.start === "number" ? created - call.start : undefined,
		});
	}

	function end(session, event, fields) {
		busy.delete(session);
		send(event, session, fields);
	}

	// why: an OpenCode idle at its first prompt has reported, so pitwall knows the plugin is in.
	send("SessionStart");

	return ctx.data.listen(({ details: event }) => {
		try {
			const d = event?.data ?? {};
			if (event?.type === "session.created") {
				if (d.parentID) children.add(d.sessionID);
				return;
			}
			const session = d.form?.sessionID ?? d.sessionID;
			if (!main(session)) return;
			switch (event.type) {
				case "session.inbox.enqueued":
					if (d.item?.type !== "user") return;
					busy.add(session);
					last.delete(session);
					send("UserPromptSubmit", session, { prompt: String(d.item.payload?.text ?? "") });
					return;
				case "session.execution.started":
					if (busy.has(session)) return;
					busy.add(session);
					send("PostToolUse", session);
					return;
				case "session.tool.input.started":
					calls.set(d.id, { tool: d.name });
					if (calls.size > 256) calls.delete(calls.keys().next().value);
					return;
				case "session.tool.called": {
					const call = calls.get(d.id) ?? {};
					calls.set(d.id, { ...call, input: d.input, start: event.created });
					send("PreToolUse", session, { tool_name: String(call.tool ?? "") });
					return;
				}
				case "session.tool.success": {
					const exit = d.metadata?.exit;
					if (typeof exit === "number" && exit !== 0) {
						const out = (d.content ?? []).map((c) => c?.text ?? "").join("\n");
						fail(session, d, event.created, out, exit);
					}
					calls.delete(d.id);
					return;
				}
				case "session.tool.failed":
					fail(session, d, event.created, d.error?.message);
					calls.delete(d.id);
					return;
				case "session.text.ended":
					last.set(session, d.text);
					return;
				case "session.execution.succeeded":
					end(session, "Stop", { last_assistant_message: text(last.get(session)) });
					return;
				case "session.execution.failed":
					end(session, "StopFailure", { error: text(d.error?.message || d.error?.type || "error") });
					return;
				case "session.execution.interrupted":
					// why: a superseded run gives way to the next one, which reports for itself.
					if (d.reason === "superseded") busy.delete(session);
					else if (d.reason === "user") end(session, "Interrupt");
					else end(session, "StopFailure", { error: `Session interrupted: ${d.reason}` });
					return;
				case "permission.asked":
					send("Notification", session, { notification_type: "permission_prompt", message: String(d.action ?? "") });
					return;
				case "form.created": {
					const field = d.form?.fields?.[0];
					send("Notification", session, { notification_type: "elicitation_dialog", message: String(field?.description || field?.title || d.form?.title || "question") });
					return;
				}
				case "permission.replied":
				case "form.replied":
				case "form.cancelled":
					send("PostToolUse", session);
					return;
			}
		} catch {}
	});
}

// why: OpenCode 1.4 and later read server; OpenCode 2 reads id and setup and ignores server.
export default { id: "pitwall", server, setup: tui };
