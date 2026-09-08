// Run only against the dedicated local certification database; no API, delivery
// worker, or real account is used. Existing model credentials stay in the child env.
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
const cfg = Object.fromEntries(readFileSync('.env', 'utf8').split(/\r?\n/)
  .map(line => line.match(/^([A-Z_0-9]+)=(.*)$/)).filter(Boolean)
  .map(([, key, value]) => [key, value.trim().replace(/^(['"])(.*)\1$/, '$2')]));
const db = new URL(cfg.DATABASE_URL);
if (!['localhost', '127.0.0.1', '[::1]'].includes(db.hostname)) throw new Error('Local database required');
db.pathname = '/tellbook_tessa_b2_certification';
const filter = process.argv[2] || 'calendar|operations|boundaries';
if (!/^[a-z|]+$/.test(filter)) throw new Error('Pass scenario names separated by |');
const mode = process.argv[3] || 'configured';
if (!['configured', 'external'].includes(mode)) throw new Error('Model mode must be configured or external');
const child = spawnSync('go', ['test', './internal/appdata', '-run', `^TestTessaConversationLive$/^(${filter})$`, '-count=1', '-v', '-timeout=35m'], {
  env: { ...process.env, ...cfg, DATABASE_URL: db.href, TEST_DATABASE_URL: db.href, RUN_TESSA_CONVERSATION_EVAL: 'true', TESSA_EVAL_MODEL_MODE: mode },
  stdio: 'inherit',
});
process.exit(child.status ?? 1);
