// pitwall's pi extension, from `pitwall hooks install`; edited copies are left alone.
import { type ChildProcess, spawn } from "node:child_process";

const bin = __PITWALL_BIN__;

type Job = { payload: Record<string, unknown>; done: () => void };

// why: one `pitwall hook pi` at a time keeps events in order; pi waits only at shutdown, up to 1s.
const limit = 32;
let pending: Job[] = [];
let busy = false;
let current: ChildProcess | undefined;
let session: { session_id?: string; ephemeral?: boolean } = {};
let last: { stop_reason?: string; message?: string; error?: string } = {};

function run(payload: Record<string, unknown>): Promise<void> {
	return new Promise<void>((resolve) => {
		let timer: ReturnType<typeof setTimeout> | undefined;
		let settled = false;
		const done = () => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			current = undefined;
			resolve();
		};
		try {
			const child = spawn(bin, ["hook", "pi"], { stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
			current = child;
			// warn: a child may ignore SIGTERM, so the deadline kills it and moves on regardless.
			timer = setTimeout(() => {
				kill(child);
				done();
			}, 5000);
			// why: a report never keeps pi from exiting.
			child.unref();
			(child.stdin as any)?.unref?.();
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

function kill(child: ChildProcess | undefined): void {
	try {
		child?.kill("SIGKILL");
	} catch {}
}

function pump(): void {
	if (busy) return;
	const job = pending.shift();
	if (!job) return;
	busy = true;
	run(job.payload).then(() => {
		busy = false;
		job.done();
		pump();
	});
}

// send queues one report and resolves once it ran or was dropped.
function send(event: string, fields: Record<string, unknown> = {}): Promise<void> {
	return new Promise<void>((resolve) => {
		pending.push({ payload: { event, ...session, ...fields }, done: resolve });
		while (pending.length > limit) pending.shift()!.done();
		pump();
	});
}

function text(message: any): string {
	if (!message || !Array.isArray(message.content)) return "";
	const parts = message.content.filter((c: any) => c && c.type === "text").map((c: any) => String(c.text));
	return parts.join("\n").trim().slice(0, 200);
}

export default function (pi: any) {
	if (!process.env.PITWALL_PANE) return;
	pi.on("session_start", (_event: any, ctx: any) => {
		try {
			// why: an ephemeral session (--no-session) has no file and cannot be resumed.
			session = { session_id: String(ctx.sessionManager.getSessionId() ?? ""), ephemeral: !ctx.sessionManager.getSessionFile() };
		} catch {
			session = {};
		}
		send("session_start");
	});
	pi.on("before_agent_start", (event: any) => {
		send("before_agent_start", { prompt: typeof event?.prompt === "string" ? event.prompt : "" });
	});
	pi.on("agent_start", () => {
		last = {};
		send("agent_start");
	});
	pi.on("tool_call", (event: any) => {
		send("tool_call", { tool_name: String(event?.toolName ?? "") });
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
		send("agent_settled", last);
	});
	pi.on("session_shutdown", async (event: any) => {
		// why: reports still queued belong to the ending session; only its shutdown goes out.
		for (const job of pending.splice(0)) job.done();
		await Promise.race([send("session_shutdown"), new Promise((r) => setTimeout(r, 1000))]);
		if (event?.reason === "quit") kill(current); // pi exits next, so nothing would reap a stuck hook
	});
}
