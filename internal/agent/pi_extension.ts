// pitwall's pi and omp extension, from `pitwall hooks install`; edited copies are left alone.
import { type ChildProcess, spawn } from "node:child_process";
import { randomUUID } from "node:crypto";

const bin = __PITWALL_BIN__;
// agent is the copy's agent: pi, or omp for Oh My Pi.
const agent: string = "pi";
// why: omp titles its process "omp", and loads pi's copy when $PI_CODING_AGENT_DIR points it at pi's dir.
const provider = agent === "omp" || process.title === "omp" ? "omp" : "pi";

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

// check runs `pitwall guard` on a shell command in a guarded tab and resolves
// to the reason it denies it, or "" when it allows it or cannot run.
function check(command: string): Promise<string> {
	return new Promise<string>((resolve) => {
		let reason = "";
		try {
			const child = spawn(bin, ["guard"], { stdio: ["pipe", "ignore", "pipe"], windowsHide: true });
			const timer = setTimeout(() => {
				kill(child);
				resolve("");
			}, 5000);
			child.stderr?.on("data", (d: Buffer) => (reason += d));
			child.on("error", () => {
				clearTimeout(timer);
				resolve("");
			});
			child.on("close", (code: number | null) => {
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

// tail is the end of a failed tool's output, where its error is.
function tail(content: any): string {
	if (!Array.isArray(content)) return "";
	const s = content.filter((c: any) => c && c.type === "text").map((c: any) => String(c.text)).join("\n").trim();
	return Array.from(s).slice(-16000).join("");
}

export default function (pi: any) {
	if (!process.env.PITWALL_PANE) return;
	// why: /reload starts a new runtime in the same session; pitwall tells their reports apart by this.
	const runtime = randomUUID();
	// why: one `pitwall hook` at a time keeps events in order; pi waits only at shutdown, up to 1s, for the final report.
	const limit = 32;
	let pending: Job[] = [];
	let busy = false;
	let session: { session_id?: string; ephemeral?: boolean; session_file?: string } = {};
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
				const child = spawn(bin, ["hook", provider], { stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
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

	// final runs `pitwall hook` detached, so it outlives pi and pi's process
	// group, and resolves once payload is in its pipe. The hook gives up on its
	// own after 5s.
	function final(payload: Record<string, unknown>): Promise<void> {
		return new Promise<void>((resolve) => {
			try {
				const child = spawn(bin, ["hook", provider], { detached: true, stdio: ["pipe", "ignore", "ignore"], windowsHide: true });
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
			const file = ctx.sessionManager.getSessionFile();
			session = { session_id: String(ctx.sessionManager.getSessionId() ?? ""), ephemeral: !file, session_file: file ? String(file) : "" };
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
	pi.on("tool_call", async (event: any, ctx: any) => {
		report(ctx, "tool_call", { tool_name: String(event?.toolName ?? "") });
		const command = event?.input?.command;
		// why: a tool_call handler that returns block stops the call, and the model reads the reason.
		if (process.env.PITWALL_GUARD && typeof command === "string") {
			const reason = await check(command);
			if (reason) return { block: true, reason };
		}
	});
	pi.on("tool_result", (event: any, ctx: any) => {
		if (event?.isError !== true) return;
		const input = event.input ?? {};
		// why: pi puts bash's exit code in structuredContent, omp in details.
		const exit = event.structuredContent?.exit_code ?? event.details?.exitCode;
		report(ctx, "tool_error", {
			tool_name: String(event.toolName ?? ""),
			tool_input: { command: typeof input.command === "string" ? input.command : undefined, path: typeof input.path === "string" ? input.path : undefined },
			error: tail(event.content),
			exit_code: typeof exit === "number" ? exit : undefined,
			cwd: typeof ctx?.cwd === "string" ? ctx.cwd : undefined,
		});
	});
	function settle(ctx: any): void {
		result = last;
		report(ctx, "agent_settled", last);
	}
	pi.on("agent_end", (event: any, ctx: any) => {
		try {
			const messages = Array.isArray(event?.messages) ? event.messages : [];
			const m = [...messages].reverse().find((m: any) => m && m.role === "assistant");
			last = m ? { stop_reason: m.stopReason, message: text(m), error: m.errorMessage } : {};
		} catch {
			last = {};
		}
		// why: omp has no agent_settled; a run's last agent_end is the one that will not continue.
		if (provider === "omp" && event?.willContinue !== true) settle(ctx);
	});
	pi.on("agent_settled", (_event: any, ctx: any) => settle(ctx));
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
