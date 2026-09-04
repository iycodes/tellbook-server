#!/usr/bin/env node

import { randomUUID } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const MAX_CONCURRENT_STREAMS = 10_000;

function usage() {
  console.error(
    "Usage: node scripts/inbox-load.mjs --config <file> [--out <file>] [--allow-http] [--confirm-write]",
  );
}

function parseArgs(argv) {
  const args = { allowHttp: false, confirmWrite: false, config: "", out: "" };
  for (let index = 0; index < argv.length; index += 1) {
    switch (argv[index]) {
      case "--config":
        args.config = argv[++index] ?? "";
        break;
      case "--out":
        args.out = argv[++index] ?? "";
        break;
      case "--allow-http":
        args.allowHttp = true;
        break;
      case "--confirm-write":
        args.confirmWrite = true;
        break;
      default:
        throw new Error(`Unknown argument: ${argv[index]}`);
    }
  }
  if (!args.config) throw new Error("--config is required");
  return args;
}

function positiveNumber(
  value,
  name,
  { integer = false, allowZero = false } = {},
) {
  const parsed = Number(value);
  if (
    !Number.isFinite(parsed) ||
    (allowZero ? parsed < 0 : parsed <= 0) ||
    (integer && !Number.isInteger(parsed))
  ) {
    throw new Error(
      `${name} must be ${allowZero ? "a non-negative" : "a positive"}${integer ? " integer" : " number"}`,
    );
  }
  return parsed;
}

function loadInputs(args) {
  const configPath = resolve(args.config);
  const config = JSON.parse(readFileSync(configPath, "utf8"));
  const baseURL = new URL(String(config.base_url ?? ""));
  if (
    baseURL.protocol !== "https:" &&
    !(args.allowHttp && baseURL.protocol === "http:")
  ) {
    throw new Error(
      "base_url must use HTTPS (or pass --allow-http for a local target)",
    );
  }
  const identitiesPath = resolve(
    configPath,
    "..",
    String(config.identities_file ?? ""),
  );
  let identities = readFileSync(identitiesPath, "utf8")
    .split(/\r?\n/u)
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line, index) => {
      const identity = JSON.parse(line);
      if (!["provider", "marketplace_customer"].includes(identity.actor_type)) {
        throw new Error(`identity ${index + 1} has an invalid actor_type`);
      }
      if (
        !identity.headers ||
        typeof identity.headers !== "object" ||
        Array.isArray(identity.headers)
      ) {
        throw new Error(
          `identity ${index + 1} must provide authentication headers`,
        );
      }
      if (identity.streams !== undefined) {
        positiveNumber(identity.streams, `identity ${index + 1} streams`, {
          integer: true,
        });
        if (identity.streams > 3) {
          throw new Error(
            `identity ${index + 1} streams cannot exceed the server cap of 3`,
          );
        }
      }
      return identity;
    });
  if (identities.length === 0)
    throw new Error("identities_file contains no identities");
  if (config.identity_limit !== undefined) {
    const identityLimit = positiveNumber(
      config.identity_limit,
      "identity_limit",
      { integer: true },
    );
    identities = identities.slice(0, identityLimit);
  }

  const options = {
    baseURL: baseURL.toString().replace(/\/$/u, ""),
    durationMs:
      positiveNumber(config.duration_seconds ?? 300, "duration_seconds") * 1000,
    rampMs:
      positiveNumber(config.ramp_seconds ?? 60, "ramp_seconds", {
        allowZero: true,
      }) * 1000,
    streamsPerIdentity: positiveNumber(
      config.streams_per_identity ?? 1,
      "streams_per_identity",
      { integer: true },
    ),
    reconnectEachStream: config.reconnect_each_stream === true,
    reconnectAfterMs:
      positiveNumber(
        config.reconnect_after_seconds ?? 30,
        "reconnect_after_seconds",
      ) * 1000,
    messageRate: positiveNumber(
      config.message_rate_per_second ?? 0,
      "message_rate_per_second",
      { allowZero: true },
    ),
    maxInFlightMessages: positiveNumber(
      config.max_in_flight_messages ?? 200,
      "max_in_flight_messages",
      { integer: true },
    ),
    readRate: positiveNumber(
      config.read_probe_rate_per_second ?? 0,
      "read_probe_rate_per_second",
      { allowZero: true },
    ),
    maxInFlightReads: positiveNumber(
      config.max_in_flight_reads ?? 200,
      "max_in_flight_reads",
      { integer: true },
    ),
    identities,
  };
  if (options.streamsPerIdentity > 3) {
    throw new Error("streams_per_identity cannot exceed the server cap of 3");
  }
  const targetStreams = identities.reduce(
    (total, identity) => total + (identity.streams ?? options.streamsPerIdentity),
    0,
  );
  if (targetStreams > MAX_CONCURRENT_STREAMS) {
    throw new Error(
      `scenario requests ${targetStreams} streams; the current load-runner ceiling is ${MAX_CONCURRENT_STREAMS}`,
    );
  }
  if (options.messageRate > 0 && !args.confirmWrite) {
    throw new Error(
      "message load creates real messages; pass --confirm-write after selecting disposable staging conversations",
    );
  }
  if (
    options.messageRate > 0 &&
    !identities.some((identity) => identity.conversation_id)
  ) {
    throw new Error(
      "message load requires at least one identity with conversation_id",
    );
  }
  return options;
}

function endpoint(options, identity, kind) {
  const provider = identity.actor_type === "provider";
  if (kind === "events") {
    return `${options.baseURL}${provider ? "/app/inbox/events" : "/marketplace/conversations/events"}?after=0`;
  }
  if (kind === "list") {
    return `${options.baseURL}${provider ? "/app/inbox/conversations" : "/marketplace/conversations"}?limit=50`;
  }
  if (kind === "unread") {
    return `${options.baseURL}${provider ? "/app/inbox/unread-count" : "/marketplace/conversations/unread-count"}`;
  }
  const id = encodeURIComponent(identity.conversation_id);
  if (kind === "detail") {
    return `${options.baseURL}${provider ? "/app/inbox/conversations" : "/marketplace/conversations"}/${id}?limit=50`;
  }
  return `${options.baseURL}${provider ? "/app/inbox/conversations" : "/marketplace/conversations"}/${id}/messages`;
}

function percentile(values, fraction) {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[
    Math.min(sorted.length - 1, Math.ceil(sorted.length * fraction) - 1)
  ];
}

function consumeSSEChunk(state, chunk) {
  state.buffer += chunk;
  let boundary = state.buffer.indexOf("\n\n");
  while (boundary >= 0) {
    const frame = state.buffer.slice(0, boundary);
    state.buffer = state.buffer.slice(boundary + 2);
    const eventLine = frame
      .split("\n")
      .find((line) => line.startsWith("event:"));
    if (eventLine) {
      const event = eventLine.slice(6).trim();
      state.metrics.sse_events[event] =
        (state.metrics.sse_events[event] ?? 0) + 1;
    }
    boundary = state.buffer.indexOf("\n\n");
  }
}

async function openStream(options, identity, metrics, signal) {
  metrics.sse_attempted += 1;
  const started = performance.now();
  try {
    const response = await fetch(endpoint(options, identity, "events"), {
      headers: { Accept: "text/event-stream", ...identity.headers },
      signal,
    });
    metrics.sse_statuses[response.status] =
      (metrics.sse_statuses[response.status] ?? 0) + 1;
    if (!response.ok || !response.body) {
      metrics.sse_failures += 1;
      await response.body?.cancel();
      return;
    }
    metrics.sse_connected += 1;
    metrics.sse_connect_ms.push(performance.now() - started);
    const state = { buffer: "", metrics };
    const decoder = new TextDecoder();
    for await (const chunk of response.body)
      consumeSSEChunk(state, decoder.decode(chunk, { stream: true }));
    if (!signal.aborted) metrics.sse_unexpected_closes += 1;
  } catch (error) {
    if (!signal.aborted) {
      metrics.sse_failures += 1;
      metrics.errors.push(
        String(error instanceof Error ? error.message : error).slice(0, 240),
      );
    }
  }
}

async function runControlledStream(options, identity, metrics, signal) {
  if (!options.reconnectEachStream) {
    await openStream(options, identity, metrics, signal);
    return;
  }
  const firstAttempt = new AbortController();
  const stopFirstAttempt = () => firstAttempt.abort();
  signal.addEventListener("abort", stopFirstAttempt, { once: true });
  const reconnectTimer = setTimeout(stopFirstAttempt, options.reconnectAfterMs);
  await openStream(options, identity, metrics, firstAttempt.signal);
  clearTimeout(reconnectTimer);
  signal.removeEventListener("abort", stopFirstAttempt);
  if (signal.aborted) return;
  metrics.sse_reconnects += 1;
  await openStream(options, identity, metrics, signal);
}

async function runMessages(options, metrics, signal) {
  if (options.messageRate === 0) return;
  const writers = options.identities.filter(
    (identity) => identity.conversation_id,
  );
  const intervalMs = 1000 / options.messageRate;
  let writerIndex = 0;
  let nextAt = performance.now();
  const requests = new Set();
  while (!signal.aborted) {
    const waitMs = nextAt - performance.now();
    if (waitMs > 0) await delay(waitMs, undefined, { signal }).catch(() => {});
    if (signal.aborted) break;
    nextAt += intervalMs;
    if (requests.size >= options.maxInFlightMessages) {
      metrics.message_shed += 1;
      continue;
    }
    const identity = writers[writerIndex++ % writers.length];
    const started = performance.now();
    const request = fetch(endpoint(options, identity, "messages"), {
      method: "POST",
      headers: { "Content-Type": "application/json", ...identity.headers },
      body: JSON.stringify({
        client_message_id: randomUUID(),
        content: `Inbox load test ${new Date().toISOString()}`,
      }),
      signal,
    })
      .then(async (response) => {
        metrics.message_statuses[response.status] =
          (metrics.message_statuses[response.status] ?? 0) + 1;
        metrics.message_latency_ms.push(performance.now() - started);
        await response.body?.cancel();
      })
      .catch((error) => {
        if (!signal.aborted)
          metrics.errors.push(
            String(error instanceof Error ? error.message : error).slice(
              0,
              240,
            ),
          );
      })
      .finally(() => requests.delete(request));
    requests.add(request);
  }
  await Promise.allSettled(requests);
}

async function runReadProbes(options, metrics, signal) {
  if (options.readRate === 0) return;
  const intervalMs = 1000 / options.readRate;
  let identityIndex = 0;
  let actionIndex = 0;
  let nextAt = performance.now();
  const requests = new Set();
  while (!signal.aborted) {
    const waitMs = nextAt - performance.now();
    if (waitMs > 0) await delay(waitMs, undefined, { signal }).catch(() => {});
    if (signal.aborted) break;
    nextAt += intervalMs;
    if (requests.size >= options.maxInFlightReads) {
      metrics.read_shed += 1;
      continue;
    }
    const identity =
      options.identities[identityIndex++ % options.identities.length];
    const availableKinds = identity.conversation_id
      ? ["list", "detail", "unread"]
      : ["list", "unread"];
    const kind = availableKinds[actionIndex++ % availableKinds.length];
    const started = performance.now();
    const request = fetch(endpoint(options, identity, kind), {
      headers: { Accept: "application/json", ...identity.headers },
      signal,
    })
      .then(async (response) => {
        const statuses = metrics.read_statuses[kind];
        statuses[response.status] = (statuses[response.status] ?? 0) + 1;
        metrics.read_latency_ms[kind].push(performance.now() - started);
        await response.body?.cancel();
      })
      .catch((error) => {
        if (!signal.aborted)
          metrics.errors.push(
            String(error instanceof Error ? error.message : error).slice(
              0,
              240,
            ),
          );
      })
      .finally(() => requests.delete(request));
    requests.add(request);
  }
  await Promise.allSettled(requests);
}

function latencySummary(values) {
  return {
    p50: Number(percentile(values, 0.5).toFixed(2)),
    p95: Number(percentile(values, 0.95).toFixed(2)),
    p99: Number(percentile(values, 0.99).toFixed(2)),
    max: Number(
      values.reduce((largest, value) => Math.max(largest, value), 0).toFixed(2),
    ),
  };
}

async function main() {
  let args;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (error) {
    usage();
    throw error;
  }
  const options = loadInputs(args);
  const metrics = {
    started_at: new Date().toISOString(),
    target_duration_seconds: options.durationMs / 1000,
    target_streams: options.identities.reduce(
      (total, identity) => total + (identity.streams ?? options.streamsPerIdentity),
      0,
    ),
    target_message_rate_per_second: options.messageRate,
    target_read_probe_rate_per_second: options.readRate,
    sse_attempted: 0,
    sse_connected: 0,
    sse_failures: 0,
    sse_unexpected_closes: 0,
    sse_reconnects: 0,
    sse_statuses: {},
    sse_events: {},
    sse_connect_ms: [],
    message_statuses: {},
    message_latency_ms: [],
    message_shed: 0,
    read_statuses: { list: {}, detail: {}, unread: {} },
    read_latency_ms: { list: [], detail: [], unread: [] },
    read_shed: 0,
    errors: [],
  };
  const controller = new AbortController();
  const streams = [];
  const streamTargets = options.identities.flatMap((identity) =>
    Array.from(
      { length: identity.streams ?? options.streamsPerIdentity },
      () => identity,
    ),
  );
  const rampInterval =
    streamTargets.length > 1 ? options.rampMs / (streamTargets.length - 1) : 0;
  const loadStarted = performance.now();
  for (let index = 0; index < streamTargets.length; index += 1) {
    const dueAt = loadStarted + index * rampInterval;
    const waitMs = dueAt - performance.now();
    if (waitMs > 0) await delay(waitMs);
    streams.push(
      runControlledStream(
        options,
        streamTargets[index],
        metrics,
        controller.signal,
      ),
    );
  }
  const messages = runMessages(options, metrics, controller.signal);
  const reads = runReadProbes(options, metrics, controller.signal);
  await delay(options.durationMs);
  controller.abort();
  await Promise.allSettled([...streams, messages, reads]);

  const report = {
    ...metrics,
    finished_at: new Date().toISOString(),
    observed_duration_seconds: Number(
      ((performance.now() - loadStarted) / 1000).toFixed(3),
    ),
    sse_connect_latency_ms: latencySummary(metrics.sse_connect_ms),
    message_latency_summary_ms: latencySummary(metrics.message_latency_ms),
    read_latency_summary_ms: Object.fromEntries(
      Object.entries(metrics.read_latency_ms).map(([kind, values]) => [
        kind,
        latencySummary(values),
      ]),
    ),
  };
  delete report.sse_connect_ms;
  delete report.message_latency_ms;
  delete report.read_latency_ms;
  report.errors = [...new Set(report.errors)].slice(0, 20);
  const output = `${JSON.stringify(report, null, 2)}\n`;
  if (args.out)
    writeFileSync(resolve(args.out), output, { encoding: "utf8", mode: 0o600 });
  process.stdout.write(output);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});
