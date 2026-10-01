// Load test (test-plan §9, D-21): a constant arrival rate of wager operations
// against app-1..3, then the correctness gates (teardown): the outbox drains
// and every wallet reconciles. Run it with make load-test.
import http from 'k6/http';
import exec from 'k6/execution';
import { sleep } from 'k6';
import { Counter, Gauge, Rate } from 'k6/metrics';

const RATE = parseInt(__ENV.RATE || '200', 10);
const DURATION = __ENV.DURATION || '60s';
const WALLETS = parseInt(__ENV.WALLETS || '1000', 10);

const APPS = ['app-1', 'app-2', 'app-3'];
const TOKEN_URL = 'http://keycloak:8080/realms/pda/protocol/openid-connect/token';
const BATCH = 50; // parallel requests per http.batch (openings and reconciliations)
const DRAIN_LIMIT_S = 60;
const KINDS = ['BET', 'WIN', 'REFUND'];
const RESULTS = [
  'processed', 'http_200', 'http_202', 'http_400', 'http_401', 'http_403', 'http_404', 'http_409', 'http_415',
  'http_422', 'http_429', 'http_500', 'http_502', 'http_503', 'http_504', 'http_other', 'timeout', 'network_error',
];
const REPORT = [
  'drain_ms', 'conflicts_lock_timeout', 'conflicts_unique_race', 'wagers_processed', 'wagers_rejected',
  'wagers_failed', 'wagers_pending_reference', 'lag_events', 'lag_mean_ms', 'wallets_reconciled',
  'wallets_divergent',
];

const outcomes = new Counter('load_outcomes');
const errors = new Rate('load_errors');
// Values computed by teardown() and read back by handleSummary().
const report = {};
for (const name of REPORT) report[name] = new Gauge(`load_${name}`);

// A submetric reaches the summary only when a threshold names it, hence the
// always-true thresholds. load_errors is the only real one (spec decision 6).
const thresholds = {
  load_errors: ['rate<0.01'],
  'http_reqs{phase:load}': ['count>=0'],
  'http_req_duration{phase:load}': ['max>=0'],
};
for (const kind of KINDS) {
  thresholds[`http_reqs{phase:load,kind:${kind}}`] = ['count>=0'];
  thresholds[`http_req_duration{phase:load,kind:${kind}}`] = ['max>=0'];
}
for (const result of RESULTS) thresholds[`load_outcomes{result:${result}}`] = ['count>=0'];

export const options = {
  scenarios: {
    load: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: RATE,
      maxVUs: 2 * RATE,
      tags: { phase: 'load' },
    },
  },
  thresholds,
  summaryTrendStats: ['med', 'p(95)', 'p(99)', 'max'],
  setupTimeout: '180s',
  teardownTimeout: '300s',
};

// --- helpers ---------------------------------------------------------------

function money(cents) {
  return `${Math.floor(cents / 100)}.${String(cents % 100).padStart(2, '0')}`;
}

function randInt(min, max) {
  return min + Math.floor(Math.random() * (max - min + 1));
}

function pick(items) {
  return items[Math.floor(Math.random() * items.length)];
}

function token(clientId, secretVar) {
  const secret = __ENV[secretVar];
  if (!secret) throw new Error(`${secretVar} is not set (env_file of the k6 service)`);
  const res = http.post(TOKEN_URL, { grant_type: 'client_credentials', client_id: clientId, client_secret: secret });
  if (res.status !== 200) throw new Error(`token for ${clientId}: HTTP ${res.status}`);
  return res.json('access_token');
}

function authHeaders(tok) {
  return { Authorization: `Bearer ${tok}`, 'Content-Type': 'application/json' };
}

// Parses the Prometheus text format of one replica into samples.
function scrape(app) {
  const res = http.get(`http://${app}:9090/metrics`);
  if (res.status !== 200) throw new Error(`${app} /metrics: HTTP ${res.status}`);
  const samples = [];
  for (const line of res.body.split('\n')) {
    if (line === '' || line.charAt(0) === '#') continue;
    const m = /^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*)\})?\s+(\S+)/.exec(line);
    if (!m) continue;
    const labels = {};
    const re = /([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\.)*)"/g;
    let l;
    while ((l = re.exec(m[2] || '')) !== null) labels[l[1]] = l[2];
    samples.push({ name: m[1], labels, value: parseFloat(m[3]) });
  }
  return samples;
}

function add(totals, key, value) {
  totals[key] = (totals[key] || 0) + value;
}

// The counters the report needs, summed over the 3 replicas: each replica
// exposes only its own (M11). Every replica reports the same global outbox
// backlog, so pending is the largest reading.
function counters() {
  const c = { conflicts: {}, wagers: {}, lagCount: 0, lagSumS: 0, pending: 0 };
  for (const app of APPS) {
    for (const s of scrape(app)) {
      if (s.name === 'concurrency_conflicts_total') add(c.conflicts, s.labels.reason, s.value);
      else if (s.name === 'wager_transactions_total') add(c.wagers, s.labels.outcome, s.value);
      else if (s.name === 'outbox_publish_lag_seconds_count') c.lagCount += s.value;
      else if (s.name === 'outbox_publish_lag_seconds_sum') c.lagSumS += s.value;
      else if (s.name === 'outbox_pending_events') c.pending = Math.max(c.pending, s.value);
    }
  }
  return c;
}

function diff(after, before) {
  const out = {};
  for (const key of Object.keys(after)) out[key] = after[key] - (before[key] || 0);
  return out;
}

// Waits for an empty outbox, read twice in a row 1 s apart: the gauge is
// refreshed from the database at most once per second, so a single zero could
// predate the last commits. Returns the elapsed ms, or -1 past limitS.
function drain(limitS) {
  const start = Date.now();
  let zeros = 0;
  while (Date.now() - start < limitS * 1000) {
    sleep(1);
    zeros = counters().pending === 0 ? zeros + 1 : 0;
    if (zeros === 2) return Date.now() - start;
  }
  return -1;
}

// --- setup -----------------------------------------------------------------

export function setup() {
  for (const app of APPS) {
    const res = http.get(`http://${app}:8080/health/ready`);
    if (res.status !== 200) {
      throw new Error(`${app} is not ready (HTTP ${res.status}): start it with docker compose up --build -d --wait`);
    }
  }
  const internal = token('wallet-service', 'WALLET_SERVICE_SECRET');
  const wallets = [];
  for (let i = 0; i < WALLETS; i += BATCH) {
    const reqs = [];
    for (let j = i; j < Math.min(i + BATCH, WALLETS); j++) {
      const body = JSON.stringify({
        playerId: crypto.randomUUID(),
        initialBalance: { amount: '10000.00', currency: 'BRL' },
      });
      reqs.push(['POST', `http://${APPS[j % APPS.length]}:8080/wallets`, body, { headers: authHeaders(internal) }]);
    }
    for (const res of http.batch(reqs)) {
      if (res.status !== 201) throw new Error(`open wallet: HTTP ${res.status} ${res.body}`);
      wallets.push({ id: res.json('id'), player: res.json('playerId') });
    }
  }
  // The openings' events are published before the window opens, so the lag
  // metric and the SQL of scripts/load-test.sh count the same events.
  if (drain(DRAIN_LIMIT_S) < 0) throw new Error(`outbox not drained within ${DRAIN_LIMIT_S}s after the openings`);
  return {
    wallets,
    provider: token('provider-a', 'PROVIDER_A_SECRET'),
    runId: Date.now().toString(36),
    before: counters(),
    start: new Date().toISOString(),
  };
}

// --- load ------------------------------------------------------------------

// This VU's processed BETs not yet compensated: {wallet, player, ext, round, amount}.
// Only this VU references them, so no reference is ever pending or reversed twice.
const bets = [];
let loggedErrors = 0;

function classify(res) {
  if (res.status === 0) return res.error_code === 1050 ? 'timeout' : 'network_error';
  if (res.status === 200 && res.json('status') === 'PROCESSED') return 'processed';
  const result = `http_${res.status}`;
  return RESULTS.indexOf(result) >= 0 ? result : 'http_other';
}

function nextOperation(data, ext) {
  const r = Math.random();
  if (r >= 0.95 && bets.length > 0) {
    const b = bets.splice(Math.floor(Math.random() * bets.length), 1)[0];
    return { kind: 'REFUND', wallet: b.wallet, player: b.player, round: b.round, amount: b.amount, ref: b.ext };
  }
  if (r >= 0.7 && r < 0.95) {
    if (bets.length > 0) {
      const b = pick(bets);
      return { kind: 'WIN', wallet: b.wallet, player: b.player, round: b.round, amount: money(randInt(50, 1000)), ref: b.ext };
    }
    const w = pick(data.wallets);
    return { kind: 'WIN', wallet: w.id, player: w.player, round: `round-${ext}`, amount: money(randInt(50, 1000)) };
  }
  // BET, and the REFUND of a VU that has no BET to compensate yet.
  const w = pick(data.wallets);
  return { kind: 'BET', wallet: w.id, player: w.player, round: `round-${ext}`, amount: money(randInt(100, 500)) };
}

export default function (data) {
  const app = APPS[exec.scenario.iterationInTest % APPS.length];
  const ext = `load-${data.runId}-${exec.vu.idInTest}-${exec.vu.iterationInScenario}`;
  const op = nextOperation(data, ext);
  const body = {
    providerId: 'provider-a',
    externalTransactionId: ext,
    playerId: op.player,
    walletId: op.wallet,
    roundId: op.round,
    gameId: 'load-test',
    kind: op.kind,
    money: { amount: op.amount, currency: 'BRL' },
  };
  if (op.ref) body.referenceExternalTransactionId = op.ref;
  const headers = authHeaders(data.provider);
  headers['Idempotency-Key'] = `provider-a:${ext}`;
  const res = http.post(`http://${app}:8080/wagering/transactions`, JSON.stringify(body), {
    headers,
    tags: { kind: op.kind },
  });
  const result = classify(res);
  outcomes.add(1, { result });
  errors.add(result !== 'processed');
  if (result === 'processed' && op.kind === 'BET') {
    bets.push({ wallet: op.wallet, player: op.player, ext, round: op.round, amount: op.amount });
  }
  if (result !== 'processed' && loggedErrors < 3) {
    loggedErrors++;
    console.warn(`${op.kind} ${ext} on ${app}: ${result} ${String(res.body).slice(0, 200)}`);
  }
}

// --- teardown: correctness gates (spec decision 6) --------------------------

function reconcileAll(wallets) {
  const internal = token('wallet-service', 'WALLET_SERVICE_SECRET');
  const headers = { Authorization: `Bearer ${internal}` };
  const divergent = [];
  let reconciled = 0;
  for (let i = 0; i < wallets.length; i += BATCH) {
    const chunk = wallets.slice(i, i + BATCH);
    const reqs = chunk.map((w, j) => [
      'POST', `http://${APPS[(i + j) % APPS.length]}:8080/wallets/${w.id}/reconciliation`, null, { headers },
    ]);
    http.batch(reqs).forEach((res, j) => {
      if (res.status === 200 && res.json('consistent') === true) reconciled++;
      else divergent.push(`${chunk[j].id} (HTTP ${res.status})`);
    });
  }
  return { reconciled, divergent };
}

export function teardown(data) {
  const drainMs = drain(DRAIN_LIMIT_S);
  report.drain_ms.add(drainMs);
  if (drainMs < 0) exec.test.fail(`outbox not drained within ${DRAIN_LIMIT_S}s after the load`);

  const after = counters();
  const conflicts = diff(after.conflicts, data.before.conflicts);
  const wagers = diff(after.wagers, data.before.wagers);
  report.conflicts_lock_timeout.add(conflicts.lock_timeout || 0);
  report.conflicts_unique_race.add(conflicts.unique_race || 0);
  report.wagers_processed.add(wagers.processed || 0);
  report.wagers_rejected.add(wagers.rejected || 0);
  report.wagers_failed.add(wagers.failed || 0);
  report.wagers_pending_reference.add(wagers.pending_reference || 0);
  const lagEvents = after.lagCount - data.before.lagCount;
  report.lag_events.add(lagEvents);
  report.lag_mean_ms.add(lagEvents > 0 ? ((after.lagSumS - data.before.lagSumS) / lagEvents) * 1000 : 0);

  const { reconciled, divergent } = reconcileAll(data.wallets);
  report.wallets_reconciled.add(reconciled);
  report.wallets_divergent.add(divergent.length);
  if (divergent.length > 0) {
    exec.test.fail(`${divergent.length} wallets not consistent, first: ${divergent[0]}`);
  }
}

// --- summary ---------------------------------------------------------------

function metric(data, name, stat) {
  const m = data.metrics[name];
  return m ? m.values[stat] : undefined;
}

function gauge(data, name) {
  return metric(data, `load_${name}`, 'value');
}

function ms(x) {
  return x === undefined ? 'n/a' : x.toFixed(1);
}

function pct(part, total) {
  return total > 0 ? `${((100 * part) / total).toFixed(1)}%` : 'n/a';
}

function durationSeconds(d) {
  const unit = { h: 3600, m: 60, s: 1 };
  const re = /(\d+)(h|m|s)/g;
  let total = 0;
  let part;
  while ((part = re.exec(d)) !== null) total += parseInt(part[1], 10) * unit[part[2]];
  return total || 1;
}

function latency(data, label, name) {
  const s = (stat) => ms(metric(data, name, stat));
  return `${(label + '        ').slice(0, 8)}p50 ${s('med')} · p95 ${s('p(95)')} · p99 ${s('p(99)')} · max ${s('max')}`;
}

function summaryText(data) {
  const setup = data.setup_data || {};
  if (!setup.start) return `pda load test — setup failed, nothing was measured (see the k6 error)\n`;
  const count = (name) => metric(data, name, 'count') || 0;
  const g = (name) => {
    const v = gauge(data, name);
    return v === undefined ? 'n/a' : String(Math.round(v));
  };
  const sent = count('http_reqs{phase:load}');
  const mix = KINDS.map((k) => {
    const n = count(`http_reqs{phase:load,kind:${k}}`);
    return `${k} ${n} (${pct(n, sent)})`;
  });
  const results = RESULTS.filter((r) => count(`load_outcomes{result:${r}}`) > 0)
    .map((r) => `${r} ${count(`load_outcomes{result:${r}}`)}`);
  const rate = metric(data, 'load_errors', 'rate');
  const thr = data.metrics.load_errors ? data.metrics.load_errors.thresholds : undefined;
  const verdict = thr ? (Object.keys(thr).every((k) => thr[k].ok) ? 'ok' : 'FAILED') : 'n/a';
  const drained = gauge(data, 'drain_ms');
  let drainText = 'drain n/a';
  if (drained !== undefined) drainText = drained < 0 ? `NOT drained in ${DRAIN_LIMIT_S}s` : `drained in ${Math.round(drained)} ms`;
  return [
    `pda load test — run ${setup.runId || 'n/a'}  rate=${RATE}/s duration=${DURATION} wallets=${WALLETS}`,
    `requests      ${sent} sent · ${(sent / durationSeconds(DURATION)).toFixed(1)}/s achieved · dropped ${count('dropped_iterations')}`,
    `mix           ${mix.join(' · ')}`,
    `latency (ms)  ${latency(data, 'all', 'http_req_duration{phase:load}')}`,
    ...KINDS.map((k) => `              ${latency(data, k, `http_req_duration{phase:load,kind:${k}}`)}`),
    `outcomes      ${results.join(' · ') || 'none'} · error rate ${rate === undefined ? 'n/a' : `${(100 * rate).toFixed(2)}%`} (< 1%: ${verdict})`,
    `conflicts     lock_timeout ${g('conflicts_lock_timeout')} · unique_race ${g('conflicts_unique_race')}   (delta, 3 replicas)`,
    `wagers        processed ${g('wagers_processed')} · rejected ${g('wagers_rejected')} · failed ${g('wagers_failed')} · pending_reference ${g('wagers_pending_reference')}   (delta)`,
    `outbox        ${drainText} · lag metric: ${g('lag_events')} events, mean ${ms(gauge(data, 'lag_mean_ms'))} ms`,
    `consistency   ${g('wallets_reconciled')}/${setup.wallets ? setup.wallets.length : 'n/a'} wallets reconciled, ${g('wallets_divergent')} divergent`,
    '',
  ].join('\n');
}

export function handleSummary(data) {
  const text = summaryText(data);
  const setup = data.setup_data || {};
  // The raw data keeps the wallets but not the provider token (OBS-02).
  const raw = Object.assign({}, data, { setup_data: Object.assign({}, setup, { provider: undefined }) });
  const files = { stdout: text, '/out/summary.txt': text, '/out/summary.json': JSON.stringify(raw, null, 2) };
  if (setup.start) {
    const lagEvents = gauge(data, 'lag_events');
    files['/out/window.env'] = [
      `LOAD_RUN_ID=${setup.runId}`,
      `LOAD_START=${setup.start}`,
      `LOAD_END=${new Date().toISOString()}`,
      `LOAD_LAG_EVENTS=${lagEvents === undefined ? '' : Math.round(lagEvents)}`,
      '',
    ].join('\n');
  }
  return files;
}
