import http from 'k6/http';
import exec from 'k6/execution';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const USERNAME = __ENV.ADMIN_USER || 'admin';
const PASSWORD = __ENV.ADMIN_PASS || '1234567891011121314';
const DB_ID_A = __ENV.DB_ID_A || '1';
const DB_ID_B = __ENV.DB_ID_B || '2';
const POOL_SIZE = Number(__ENV.POOL_SIZE || '45');

const apiLatency = new Trend('api_response_time_ms', true);
const stage1BaselineMs = new Trend('stage1_baseline_10vu_ms', true);
const stage2ElevatedMs = new Trend('stage2_elevated_30vu_ms', true);
const stage3PeakMs = new Trend('stage3_peak_60vu_ms', true);
const stage4RecoveryMs = new Trend('stage4_recovery_10vu_ms', true);
const apiErrorRate = new Rate('api_error_rate');
const queueWaitMs = new Trend('job_queue_wait_ms', true);
const jobDurationMs = new Trend('job_completion_duration_ms', true);
const jobsCompleted = new Counter('jobs_completed_total');

export const options = {
  noCookiesReset: true,
  stages: [
    { duration: '20s', target: 10 }, // Stage 1 (0-20s): Normal baseline ramp-up (0 -> 10 VUs)
    { duration: '30s', target: 30 }, // Stage 2 (20-50s): Elevated load above baseline (10 -> 30 VUs)
    { duration: '30s', target: 60 }, // Stage 3 (50-80s): Peak stress load (30 -> 60 VUs)
    { duration: '20s', target: 10 }, // Stage 4 (80-100s): Recovery ramp-down (60 -> 10 VUs)
    { duration: '10s', target: 0 },  // Stage 5 (100-110s): Cooldown to zero (10 -> 0 VUs)
  ],
  thresholds: {
    http_req_duration: ['p(50)<150', 'p(95)<500'],
    http_req_failed: ['rate<0.05'],
    api_error_rate: ['rate<0.05'],
    api_response_time_ms: ['p(95)<500'],
  },
};

let vuCsrfToken = '';
let vuAuthenticated = false;

function recordLatency(ms) {
  apiLatency.add(ms);
  const elapsedSec = exec.instance.currentTestRunDuration / 1000.0;
  if (elapsedSec < 20) {
    stage1BaselineMs.add(ms);
  } else if (elapsedSec < 50) {
    stage2ElevatedMs.add(ms);
  } else if (elapsedSec < 80) {
    stage3PeakMs.add(ms);
  } else {
    stage4RecoveryMs.add(ms);
  }
}

function authenticate(trackMetrics) {
  const csrfRes = http.get(`${BASE_URL}/api/auth/csrf`);
  if (trackMetrics) {
    recordLatency(csrfRes.timings.duration);
  }
  let csrfToken = '';
  try {
    csrfToken = JSON.parse(csrfRes.body).csrf_token || '';
  } catch (_) {}

  const loginRes = http.post(
    `${BASE_URL}/api/auth/login`,
    JSON.stringify({ username: USERNAME, password: PASSWORD }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': csrfToken,
      },
    }
  );
  if (trackMetrics) {
    recordLatency(loginRes.timings.duration);
    apiErrorRate.add(loginRes.status >= 400);
  }

  const afterCsrf = http.get(`${BASE_URL}/api/auth/csrf`);
  try {
    csrfToken = JSON.parse(afterCsrf.body).csrf_token || csrfToken;
  } catch (_) {}
  return csrfToken;
}

function createAndApproveMigration(dbID, title, sqlUp, sqlDown, csrfToken) {
  const headers = {
    'Content-Type': 'application/json',
    'X-CSRF-Token': csrfToken,
  };
  const createRes = http.post(
    `${BASE_URL}/api/v2/databases/${dbID}/migrations`,
    JSON.stringify({
      title,
      sql_up: sqlUp,
      sql_down: sqlDown,
      irreversible_reason: '',
    }),
    { headers }
  );
  if (createRes.status !== 201) {
    return null;
  }
  const reqObj = JSON.parse(createRes.body);
  const submitRes = http.post(
    `${BASE_URL}/api/v2/databases/${dbID}/requests/${reqObj.id}/submit`,
    '{}',
    { headers }
  );
  if (submitRes.status !== 200) {
    return null;
  }
  const approveRes = http.post(
    `${BASE_URL}/api/v2/databases/${dbID}/requests/${reqObj.id}/approve`,
    JSON.stringify({ revision_id: reqObj.revision_id }),
    { headers }
  );
  if (approveRes.status !== 200) {
    return null;
  }
  const approved = JSON.parse(approveRes.body);
  if (approved.status !== 'approved') {
    return null;
  }
  return { id: approved.id, revision_id: approved.revision_id, db_id: dbID };
}

function waitJobSync(jobID, maxPolls) {
  for (let i = 0; i < maxPolls; i++) {
    sleep(0.2);
    const r = http.get(`${BASE_URL}/api/v2/jobs/${jobID}`);
    if (r.status === 200) {
      const body = JSON.parse(r.body);
      const st = body.job ? body.job.status : '';
      if (st === 'success' || st === 'failed' || st === 'cancelled' || st === 'rejected') {
        return st;
      }
    }
  }
  return 'timeout';
}

// Pre-provision a pool of Approved migrations on DB_ID_A and DB_ID_B before load stages begin.
export function setup() {
  const csrfToken = authenticate(false);
  const headers = {
    'Content-Type': 'application/json',
    'X-CSRF-Token': csrfToken,
  };
  const runTag = Date.now();

  for (const dbID of [DB_ID_A, DB_ID_B]) {
    const boot = createAndApproveMigration(
      dbID,
      `k6-bootstrap-${runTag}-db${dbID}`,
      'CREATE TABLE IF NOT EXISTS k6_stress_probe (slot BIGINT PRIMARY KEY, note TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())',
      'DROP TABLE IF EXISTS k6_stress_probe',
      csrfToken
    );
    if (boot) {
      const ex = http.post(
        `${BASE_URL}/api/v2/databases/${dbID}/requests/${boot.id}/execute`,
        JSON.stringify({ revision_id: boot.revision_id, mode: 'normal' }),
        { headers }
      );
      if (ex.status === 202) {
        const jb = JSON.parse(ex.body);
        waitJobSync(jb.job_id, 50);
      }
    }
  }

  const poolA = [];
  const poolB = [];
  for (let i = 1; i <= POOL_SIZE; i++) {
    const slot = runTag * 1000 + i;
    const itemA = createAndApproveMigration(
      DB_ID_A,
      `k6-approved-a-${slot}`,
      `INSERT INTO k6_stress_probe (slot, note) VALUES (${slot}, 'applied') ON CONFLICT (slot) DO UPDATE SET note = 'applied', updated_at = now()`,
      `DELETE FROM k6_stress_probe WHERE slot = ${slot}`,
      csrfToken
    );
    if (itemA) poolA.push(itemA);

    const itemB = createAndApproveMigration(
      DB_ID_B,
      `k6-approved-b-${slot}`,
      `INSERT INTO k6_stress_probe (slot, note) VALUES (${slot}, 'applied') ON CONFLICT (slot) DO UPDATE SET note = 'applied', updated_at = now()`,
      `DELETE FROM k6_stress_probe WHERE slot = ${slot}`,
      csrfToken
    );
    if (itemB) poolB.push(itemB);
  }

  return { poolA, poolB };
}

// Only count terminal job completion, NEVER the initial 202 enqueue response.
function submitAndAwaitJob(item, csrfToken) {
  if (!item || !item.id || !item.revision_id) {
    return;
  }
  const submitStart = Date.now();
  const res = http.post(
    `${BASE_URL}/api/v2/databases/${item.db_id}/requests/${item.id}/execute`,
    JSON.stringify({ revision_id: item.revision_id, mode: 'normal' }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': csrfToken,
      },
    }
  );
  recordLatency(res.timings.duration);

  if (res.status !== 202 && res.status !== 200) {
    apiErrorRate.add(res.status >= 500);
    return;
  }
  apiErrorRate.add(false);

  let jobID = '';
  try {
    const body = JSON.parse(res.body);
    jobID = body.job_id || (body.job && body.job.id) || '';
  } catch (_) {}
  if (!jobID) {
    return;
  }

  let recordedWait = false;
  for (let i = 0; i < 60; i++) {
    sleep(0.25);
    const pollRes = http.get(`${BASE_URL}/api/v2/jobs/${jobID}`);
    recordLatency(pollRes.timings.duration);
    if (pollRes.status !== 200) {
      apiErrorRate.add(pollRes.status >= 500);
      continue;
    }
    apiErrorRate.add(false);
    try {
      const detail = JSON.parse(pollRes.body);
      const job = detail.job || detail;
      const st = (job.status || '').toLowerCase();
      if (!recordedWait && job.queued_at && job.started_at) {
        const wait = Math.max(0, Date.parse(job.started_at) - Date.parse(job.queued_at));
        if (!Number.isNaN(wait)) {
          queueWaitMs.add(wait);
          recordedWait = true;
        }
      }
      if (st === 'success') {
        const now = Date.now();
        if (!recordedWait) {
          queueWaitMs.add(now - submitStart);
        }
        if (job.queued_at && job.finished_at) {
          const totalDur = Math.max(1, Date.parse(job.finished_at) - Date.parse(job.queued_at));
          jobDurationMs.add(Number.isNaN(totalDur) ? now - submitStart : totalDur);
        } else {
          jobDurationMs.add(now - submitStart);
        }
        jobsCompleted.add(1);
        return;
      }
      if (st === 'failed' || st === 'cancelled' || st === 'rejected') {
        apiErrorRate.add(true);
        return;
      }
    } catch (_) {}
  }
}

export default function (data) {
  if (!vuAuthenticated || !vuCsrfToken) {
    // Do not include one-time bcrypt login setup in steady-state API latency metrics.
    vuCsrfToken = authenticate(false);
    vuAuthenticated = true;
  }

  const dbsRes = http.get(`${BASE_URL}/api/databases`);
  recordLatency(dbsRes.timings.duration);
  if (dbsRes.status === 401) {
    vuCsrfToken = authenticate(false);
    return;
  }
  apiErrorRate.add(dbsRes.status !== 200);
  check(dbsRes, { 'GET /api/databases is 200': (r) => r.status === 200 });

  const globalIter = exec.scenario.iterationInTest;
  const targetDB = globalIter % 2 === 0 ? DB_ID_A : DB_ID_B;

  const schemaRes = http.get(`${BASE_URL}/api/databases/${targetDB}/schema`);
  recordLatency(schemaRes.timings.duration);
  apiErrorRate.add(schemaRes.status !== 200);

  const histRes = http.get(`${BASE_URL}/api/v2/databases/${targetDB}/history?hide_rollback=false`);
  recordLatency(histRes.timings.duration);
  apiErrorRate.add(histRes.status !== 200);

  const jobsListRes = http.get(`${BASE_URL}/api/v2/jobs?limit=20`);
  recordLatency(jobsListRes.timings.duration);
  apiErrorRate.add(jobsListRes.status !== 200);

  const auditRes = http.get(`${BASE_URL}/api/audit-logs?page=1&page_size=10`);
  recordLatency(auditRes.timings.duration);
  apiErrorRate.add(auditRes.status !== 200);
  check(auditRes, { 'GET /api/audit-logs is 200': (r) => r.status === 200 });

  // Dispatch pre-approved migrations concurrently on SAME DB (paired bursts on DB_ID_A)
  // and DIFFERENT DBs (simultaneous dispatches on DB_ID_A and DB_ID_B).
  if (globalIter % 50 === 0 || globalIter % 50 === 1) {
    const slotIdx = Math.floor(globalIter / 50) * 2 + (globalIter % 50);
    if (data && data.poolA && slotIdx < data.poolA.length) {
      submitAndAwaitJob(data.poolA[slotIdx], vuCsrfToken);
    }
  } else if (globalIter % 50 === 2) {
    const slotIdx = Math.floor(globalIter / 50);
    if (data && data.poolB && slotIdx < data.poolB.length) {
      submitAndAwaitJob(data.poolB[slotIdx], vuCsrfToken);
    }
  }

  sleep(0.35);
}
