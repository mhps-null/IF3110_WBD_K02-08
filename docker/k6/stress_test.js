import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const USERNAME = __ENV.ADMIN_USER || 'admin';
const PASSWORD = __ENV.ADMIN_PASS || '1234567891011121314';
const DB_ID_A = __ENV.DB_ID_A || '1';
const DB_ID_B = __ENV.DB_ID_B || '2';
const APPROVED_REQ_A = __ENV.APPROVED_REQ_A || '';
const APPROVED_REQ_B = __ENV.APPROVED_REQ_B || '';

const apiLatency = new Trend('api_response_time_ms', true);
const apiErrorRate = new Rate('api_error_rate');
const queueWaitMs = new Trend('job_queue_wait_ms', true);
const jobDurationMs = new Trend('job_completion_duration_ms', true);
const jobsCompleted = new Counter('jobs_completed_total');

export const options = {
  stages: [
    { duration: '20s', target: 10 },
    { duration: '30s', target: 30 },
    { duration: '30s', target: 60 },
    { duration: '20s', target: 10 },
    { duration: '10s', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(50)<150', 'p(95)<500'],
    http_req_failed: ['rate<0.05'],
    api_error_rate: ['rate<0.05'],
    api_response_time_ms: ['p(95)<500'],
  },
};

function authenticate() {
  const csrfRes = http.get(`${BASE_URL}/api/auth/csrf`);
  apiLatency.add(csrfRes.timings.duration);
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
  apiLatency.add(loginRes.timings.duration);
  apiErrorRate.add(loginRes.status >= 400);

  const afterCsrf = http.get(`${BASE_URL}/api/auth/csrf`);
  try {
    csrfToken = JSON.parse(afterCsrf.body).csrf_token || csrfToken;
  } catch (_) {}
  return csrfToken;
}

// Only count terminal job completion, not the initial 202 response.
function submitAndAwaitJob(dbID, reqID, csrfToken) {
  if (!reqID) {
    return;
  }
  const submitStart = Date.now();
  const res = http.post(
    `${BASE_URL}/api/v2/databases/${dbID}/requests/${reqID}/execute`,
    JSON.stringify({ mode: 'normal' }),
    {
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': csrfToken,
      },
    }
  );
  apiLatency.add(res.timings.duration);

  if (res.status !== 202 && res.status !== 200) {
    // 409 is expected when another VU already queued the same request.
    apiErrorRate.add(res.status >= 500);
    return;
  }

  let jobID = '';
  try {
    const body = JSON.parse(res.body);
    jobID = body.job_id || (body.job && body.job.id) || '';
  } catch (_) {}
  if (!jobID) {
    return;
  }

  let firstRunningAt = 0;
  for (let i = 0; i < 30; i++) {
    sleep(0.3);
    const pollRes = http.get(`${BASE_URL}/api/v2/jobs/${jobID}`);
    apiLatency.add(pollRes.timings.duration);
    if (pollRes.status !== 200) {
      apiErrorRate.add(pollRes.status >= 500);
      continue;
    }
    try {
      const job = JSON.parse(pollRes.body);
      const st = (job.status || '').toLowerCase();
      if (st === 'running' && firstRunningAt === 0) {
        firstRunningAt = Date.now();
        queueWaitMs.add(firstRunningAt - submitStart);
      }
      if (st === 'success' || st === 'completed') {
        const now = Date.now();
        if (firstRunningAt === 0) {
          queueWaitMs.add(now - submitStart);
        }
        jobDurationMs.add(now - submitStart);
        jobsCompleted.add(1);
        return;
      }
      if (st === 'failed' || st === 'cancelled') {
        return;
      }
    } catch (_) {}
  }
}

export default function () {
  const csrfToken = authenticate();

  const dbsRes = http.get(`${BASE_URL}/api/databases`);
  apiLatency.add(dbsRes.timings.duration);
  apiErrorRate.add(dbsRes.status !== 200);
  check(dbsRes, { 'GET /api/databases is 200': (r) => r.status === 200 });

  const targetDB = __ITER % 2 === 0 ? DB_ID_A : DB_ID_B;
  const schemaRes = http.get(`${BASE_URL}/api/databases/${targetDB}/schema`);
  apiLatency.add(schemaRes.timings.duration);
  apiErrorRate.add(schemaRes.status >= 500);

  const auditRes = http.get(`${BASE_URL}/api/audit-logs?page=1&page_size=10`);
  apiLatency.add(auditRes.timings.duration);
  apiErrorRate.add(auditRes.status !== 200);
  check(auditRes, { 'GET /api/audit-logs is 200': (r) => r.status === 200 });

  if (__VU % 2 === 0) {
    submitAndAwaitJob(DB_ID_A, APPROVED_REQ_A, csrfToken);
  } else {
    submitAndAwaitJob(DB_ID_B, APPROVED_REQ_B, csrfToken);
  }

  sleep(0.5);
}
