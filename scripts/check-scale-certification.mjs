#!/usr/bin/env node

import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const REQUIRED_SCENARIOS = [
	'anonymous_marketplace',
	'marketplace_discovery_database',
	'edge_cached_marketplace',
	'auth_and_account',
	'provider_application',
	'booking_contention',
	'notification_phase_a',
	'inbox_and_tessa',
	'idle_sse_10000',
	'controlled_event_burst',
	'worker_backlog_and_projection',
	'cold_cache_and_stampede',
	'redis_outage',
	'listener_reconnect',
	'failed_replica',
	'soak'
];

const REQUIRED_ASSERTIONS = [
	'no_duplicate_side_effects',
	'no_database_heartbeat_work',
	'no_sustained_pool_saturation',
	'no_queue_or_event_lag_growth',
	'notification_channel_isolation',
	'notification_webhook_phone_isolation',
	'no_unbounded_memory_growth',
	'bundle_budgets_passed'
];

function finiteNonNegative(value) {
	return Number.isFinite(value) && value >= 0;
}

function main() {
	const reportPath = process.argv[2] ? resolve(process.argv[2]) : '';
	if (!reportPath) {
		console.error('Usage: node scripts/check-scale-certification.mjs <certification-report.json>');
		process.exitCode = 2;
		return;
	}
	if (!existsSync(reportPath)) throw new Error(`Certification report not found: ${reportPath}`);

	const report = JSON.parse(readFileSync(reportPath, 'utf8'));
	const failures = [];
	if (!report.environment?.name || !report.environment?.hardware || !report.environment?.commit) {
		failures.push('environment name, hardware, and commit are required');
	}
	if (!report.measured_at || Number.isNaN(Date.parse(report.measured_at))) {
		failures.push('measured_at must be an ISO timestamp');
	}
	if (!(report.capacity?.requests_per_second > 0)) {
		failures.push('measured capacity.requests_per_second must be greater than zero');
	}
	if (!(report.capacity?.concurrent_active_users > 0) || !Number.isInteger(report.capacity.concurrent_active_users)) {
		failures.push('measured capacity.concurrent_active_users must be greater than zero');
	}

	let eligibleRequests = 0;
	let unexpectedFailures = 0;
	for (const id of REQUIRED_SCENARIOS) {
		const scenario = report.scenarios?.[id];
		if (!scenario) {
			failures.push(`missing scenario: ${id}`);
			continue;
		}
		if (typeof scenario.evidence !== 'string' || !scenario.evidence.trim()) {
			failures.push(`${id}: evidence path or URL is required`);
		}
		for (const field of ['requests', 'failures', 'intentional_rejections']) {
			if (!finiteNonNegative(scenario[field]) || !Number.isInteger(scenario[field])) {
				failures.push(`${id}: ${field} must be a non-negative integer`);
			}
		}
		if (!finiteNonNegative(scenario.requests) || !finiteNonNegative(scenario.failures)) continue;
		if (scenario.requests === 0) failures.push(`${id}: requests must be greater than zero`);
		if (scenario.failures > scenario.requests) failures.push(`${id}: failures exceed requests`);
		if (scenario.intentional_rejections > scenario.failures) {
			failures.push(`${id}: intentional_rejections exceed failures`);
		}
		eligibleRequests += Math.max(0, scenario.requests - scenario.intentional_rejections);
		unexpectedFailures += Math.max(0, scenario.failures - scenario.intentional_rejections);
	}

	const errorRate = eligibleRequests > 0 ? unexpectedFailures / eligibleRequests : 1;
	if (errorRate >= 0.001) {
		failures.push(`unexpected error rate ${(errorRate * 100).toFixed(4)}% must be below 0.1%`);
	}

	for (const [field, maximum] of [
		['simple_read_p95_ms', 300],
		['write_p95_ms', 500],
		['discovery_database_p95_ms', 150],
		['edge_cached_p95_ms', 100],
		['database_acquire_wait_p95_ms', 10]
	]) {
		const value = report.latency?.[field];
		if (!finiteNonNegative(value)) failures.push(`latency.${field} is required`);
		else if (value >= maximum) failures.push(`latency.${field} ${value}ms must be below ${maximum}ms`);
	}
	if (!finiteNonNegative(report.database?.peak_pool_utilization_percent)) {
		failures.push('database.peak_pool_utilization_percent is required');
	} else if (report.database.peak_pool_utilization_percent >= 70) {
		failures.push('database peak pool utilization must remain below 70%');
	}

	for (const assertion of REQUIRED_ASSERTIONS) {
		if (report.assertions?.[assertion] !== true) failures.push(`assertion failed or missing: ${assertion}`);
	}

	if (failures.length > 0) {
		console.error(`Scale certification failed:\n- ${failures.join('\n- ')}`);
		process.exitCode = 1;
		return;
	}

	console.log(
		`Scale certification passed: ${report.capacity.requests_per_second} req/s, ${report.capacity.concurrent_active_users} concurrent active users, ${(errorRate * 100).toFixed(4)}% unexpected errors`
	);
}

try {
	main();
} catch (cause) {
	console.error(cause instanceof Error ? cause.message : cause);
	process.exitCode = 1;
}
