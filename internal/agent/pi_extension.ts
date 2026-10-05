// pitwall's pi extension, from `pitwall hooks install`; edited copies are left alone.
import { type ChildProcess, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";

const bin = __PITWALL_BIN__;

type Job = { payload: Record<string, unknown>; done: () => void };

function kill(child: ChildProcess | undefined): void {
	try {
		child?.kill("SIGKILL");
	} catch {}
}

// text is a reply's text whole for pitwall to redact, then cut. Past the
// safety cap it ends at the last whitespace, so no token is sent cut in half.
function text(message: any): string {
	if (!message || !Array.isArray(message.content)) return "";
	const parts = message.content.filter((c: any) => c && c.type === "text").map((c: any) => String(c.text));
	const s = parts.join("\n").trim();
	const cps = Array.from(s);
	if (cps.length <= 16000) return s;
	const cut = cps.slice(0, 16000).join("");
	const i = cut.search(/\s\S*$/);
	return i > 0 ? cut.slice(0, i) : "";
}

export default function (pi: any) {
	if (!process.env.PITWALL_PANE) return;
	// why: /reload starts a new runtime in the same session; pitwall tells their reports apart by this.
	const runtime = randomUUID();
	// why: one `pitwall hook pi` at a time keeps events in order; pi waits only at shutdown, up to 1s, for the final report.
	const limit = 32;
	let pending: Job[] = [];
	let busy = false;
	let session: { session_id?: string; ephemeral?: boolean } = {};
	let started = false;
	let last: { stop_reason?: string; message?: string; error?: string } = {};
	// result is the last agent_settled's fields, until the next run starts.
	let result: typeof last | undefined;

	function run(payload: Record<string, unknown>): Promise<void> {
		return new Promise<void>((resolve) => {
			let timer: ReturnType<typeof setTimeout> | undefined;
			let settled = false;
			const done = () => {
				if (settled) return;
				settled = true;
				clearTimeout(timer);
				resolve();
			};
			try {
				const child = spawn(bin, ["hook", "pi"], { stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
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

	// final runs `pitwall hook pi` detached, so it outlives pi and pi's process
	// group, and resolves once payload is in its pipe. The hook gives up on its
	// own after 5s.
	function final(payload: Record<string, unknown>): Promise<void> {
		return new Promise<void>((resolve) => {
			try {
				const child = spawn(bin, ["hook", "pi"], { detached: true, stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
				child.on("error", () => resolve());
				child.unref();
				child.stdin?.on("error", () => resolve());
				child.stdin?.end(JSON.stringify(payload), () => resolve());
				(child.stdin as any)?.unref?.();
			} catch {
				resolve();
			}
		});
	}

	// send queues one report and resolves once it ran or was dropped.
	function send(event: string, fields: Record<string, unknown> = {}): Promise<void> {
		return new Promise<void>((resolve) => {
			pending.push({ payload: { event, runtime, ...session, ...fields }, done: resolve });
			// why: the oldest reports go first, never a queued session_start, which names the runtime.
			while (pending.length > limit) pending.splice(pending[0].payload.event === "session_start" ? 1 : 0, 1)[0].done();
			pump();
		});
	}

	// invariant: a runtime's first report is session_start; pitwall takes its
	// runtime as the pane's current one and ignores every other runtime's.
	function begin(ctx: any): void {
		try {
			// why: an ephemeral session (--no-session) has no file and cannot be resumed.
			session = { session_id: String(ctx.sessionManager.getSessionId() ?? ""), ephemeral: !ctx.sessionManager.getSessionFile() };
		} catch {
			session = {};
		}
		started = true;
		send("session_start");
	}
	function report(ctx: any, event: string, fields: Record<string, unknown> = {}): void {
		if (!started) begin(ctx);
		send(event, fields);
	}

	pi.on("session_start", (_event: any, ctx: any) => begin(ctx));
	pi.on("before_agent_start", (event: any, ctx: any) => {
		report(ctx, "before_agent_start", { prompt: typeof event?.prompt === "string" ? event.prompt : "" });
	});
	pi.on("agent_start", (_event: any, ctx: any) => {
		last = {};
		result = undefined;
		report(ctx, "agent_start");
	});
	pi.on("tool_call", (event: any, ctx: any) => {
		report(ctx, "tool_call", { tool_name: String(event?.toolName ?? "") });
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
	pi.on("agent_settled", (_event: any, ctx: any) => {
		result = last;
		report(ctx, "agent_settled", last);
	});
	pi.on("session_shutdown", async () => {
		if (!started) return;
		// why: reports still queued die with pi, a fast `pi -p` run's result
		// included, so they are dropped and one final report outlives pi: the
		// shutdown, carrying the run's result if it settled. pitwall takes it as
		// that agent_settled, then the shutdown, which retires this runtime's
		// nonce, so a report still in flight that arrives later is dropped.
		for (const job of pending.splice(0)) job.done();
		// why: an unref'd deadline; a pending timer would hold pi open its full second.
		await Promise.race([final({ event: "session_shutdown", runtime, ...session, ...result }), new Promise((r) => setTimeout(r, 1000).unref?.())]);
	});
}
