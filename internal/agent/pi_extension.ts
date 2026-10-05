// pitwall's pi extension, from `pitwall hooks install`; edited copies are left alone.
import { spawn } from "node:child_process";

const bin = __PITWALL_BIN__;

let queue = Promise.resolve();
let last: { stop_reason?: string; message?: string; error?: string } = {};

// why: one `pitwall hook pi` at a time keeps events in order; pi never waits.
function send(payload: Record<string, unknown>): Promise<void> {
	if (!process.env.PITWALL_PANE) return queue;
	queue = queue.then(
		() =>
			new Promise<void>((resolve) => {
				try {
					const child = spawn(bin, ["hook", "pi"], { stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
					const timer = setTimeout(() => child.kill(), 5000);
					const done = () => {
						clearTimeout(timer);
						resolve();
					};
					child.on("error", done);
					child.on("close", done);
					child.stdin?.on("error", () => {});
					child.stdin?.end(JSON.stringify(payload));
				} catch {
					resolve();
				}
			}),
	);
	return queue;
}

function text(message: any): string {
	if (!message || !Array.isArray(message.content)) return "";
	const parts = message.content.filter((c: any) => c && c.type === "text").map((c: any) => String(c.text));
	return parts.join("\n").trim().slice(0, 200);
}

export default function (pi: any) {
	pi.on("session_start", (_event: any, ctx: any) => {
		try {
			// why: an ephemeral session (--no-session) has no file and cannot be resumed.
			const id = ctx.sessionManager.getSessionFile() ? ctx.sessionManager.getSessionId() : "";
			send({ event: "session_start", session_id: id || "" });
		} catch {}
	});
	pi.on("before_agent_start", (event: any) => {
		send({ event: "before_agent_start", prompt: typeof event?.prompt === "string" ? event.prompt : "" });
	});
	pi.on("agent_start", () => {
		last = {};
		send({ event: "agent_start" });
	});
	pi.on("tool_call", (event: any) => {
		send({ event: "tool_call", tool_name: String(event?.toolName ?? "") });
	});
	pi.on("agent_end", (event: any) => {
		try {
			const messages = Array.isArray(event?.messages) ? event.messages : [];
			const m = [...messages].reverse().find((m: any) => m && m.role === "assistant");
			last = m ? { stop_reason: m.stopReason, message: text(m), error: m.errorMessage } : {};
		} catch {
			last = {};
		}
	});
	pi.on("agent_settled", () => {
		send({ event: "agent_settled", ...last });
	});
	pi.on("session_shutdown", async () => {
		// why: pi may exit right after this handler, so wait up to a second for the report.
		await Promise.race([send({ event: "session_shutdown" }), new Promise((r) => setTimeout(r, 1000))]);
	});
}
