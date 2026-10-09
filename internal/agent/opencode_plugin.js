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

// PitwallPlugin reports to the pitwall pane it runs in, in Claude Code's hook names; outside one it does nothing.
export const PitwallPlugin = async () => {
	if (!process.env.PITWALL_PANE) return {};
	// why: one `pitwall hook opencode` at a time keeps events in order.
	const limit = 32;
	const pending = [];
	let busy = false;
	const children = new Set(); // subagent sessions, which never set the pane's state
	const replies = new Set(); // ids of assistant messages
	const last = new Map(); // session: the text of its latest reply
	const status = new Map(); // session: "busy" or "idle"
	const ended = new Set(); // sessions whose run failed or was aborted: their idle is no completion
	const plans = new Set(); // call ids of plan_exit, whose question asks to approve the plan

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

	function send(event, session, fields = {}) {
		pending.push({ hook_event_name: event, session_id: session ?? "", ...fields });
		if (pending.length > limit) pending.shift();
		pump();
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
		"tool.execute.before": async (input) => {
			try {
				if (!main(input?.sessionID)) return;
				if (input.tool === "plan_exit" && input.callID) plans.add(input.callID);
				send("PreToolUse", input.sessionID, { tool_name: String(input.tool ?? "") });
			} catch {}
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
};
