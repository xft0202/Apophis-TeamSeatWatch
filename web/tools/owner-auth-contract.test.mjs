import { once } from 'node:events';
import { after, before, test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { URL } from 'node:url';

const { fetch } = globalThis;
let server;
let baseUrl;
let authenticated = false;
const allowedOrigin = 'http://owner.local';

function sendJson(response, status, body, headers = {}) {
  response.writeHead(status, {
    'cache-control': 'no-store',
    'content-type': 'application/json',
    ...headers,
  });
  response.end(JSON.stringify(body));
}

function sendEmpty(response, status, headers = {}) {
  response.writeHead(status, { 'cache-control': 'no-store', ...headers });
  response.end();
}

function hasCsrf(request) {
  return request.headers.origin === allowedOrigin &&
    request.headers.cookie?.includes('tsw_csrf=contract-token') &&
    request.headers['x-csrf-token'] === 'contract-token';
}

function hasSession(request) {
  return request.headers.cookie?.includes('tsw_session=contract-session');
}

function readBody(request) {
  return new Promise((resolve, reject) => {
    let value = '';
    request.on('data', (chunk) => { value += chunk; });
    request.on('end', () => resolve(JSON.parse(value)));
    request.on('error', reject);
  });
}

before(async () => {
  server = http.createServer(async (request, response) => {
    const pathname = new URL(request.url ?? '/', 'http://localhost').pathname;

    if (pathname === '/api/owner/v1/csrf' && request.method === 'GET') {
      sendJson(response, 200, { token: 'contract-token' }, {
        'set-cookie': 'tsw_csrf=contract-token; Path=/api/owner/v1; SameSite=Strict',
      });
      return;
    }

    if (pathname === '/api/owner/v1/login' && request.method === 'POST') {
      const input = await readBody(request);
      if (!hasCsrf(request)) {
        sendJson(response, 403, { code: 'csrf_rejected' });
        return;
      }
      if (input.username !== 'owner' || input.password !== 'secret') {
        sendJson(response, 401, { code: 'authentication_failed' });
        return;
      }
      authenticated = true;
      sendEmpty(response, 204, {
        'set-cookie': 'tsw_session=contract-session; Path=/; SameSite=Strict',
      });
      return;
    }

    if (pathname === '/api/owner/v1/auth-status' && request.method === 'GET') {
      if (!authenticated || !hasSession(request)) {
        sendJson(response, 401, { code: 'session_expired' });
        return;
      }
      sendJson(response, 200, {
        authenticated: true,
        username: 'owner',
        passwordChangedAt: '2026-01-01T00:00:00Z',
      });
      return;
    }

    if (pathname === '/api/owner/v1/session/refresh' && request.method === 'POST') {
      if (!authenticated || !hasSession(request)) {
        sendJson(response, 401, { code: 'session_expired' });
        return;
      }
      if (!hasCsrf(request)) {
        sendJson(response, 403, { code: 'csrf_rejected' });
        return;
      }
      sendEmpty(response, 204);
      return;
    }

    if (pathname === '/api/owner/v1/logout' && request.method === 'POST') {
      if (!authenticated || !hasSession(request)) {
        sendEmpty(response, 204);
        return;
      }
      if (!hasCsrf(request)) {
        sendJson(response, 403, { code: 'csrf_rejected' });
        return;
      }
      authenticated = false;
      sendEmpty(response, 204, { 'set-cookie': 'tsw_session=; Max-Age=0; Path=/' });
      return;
    }

    sendJson(response, 404, { code: 'not_found' });
  });

  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  baseUrl = `http://127.0.0.1:${server.address().port}`;
});

after(() => server.close());

test('Owner authentication contract preserves CSRF and session boundaries', async () => {
  const csrf = await fetch(`${baseUrl}/api/owner/v1/csrf`);
  assert.equal(csrf.status, 200);
  assert.deepEqual(await csrf.json(), { token: 'contract-token' });
  const csrfCookie = csrf.headers.get('set-cookie').split(';', 1)[0];
  const mutationHeaders = {
    'content-type': 'application/json',
    cookie: csrfCookie,
    origin: allowedOrigin,
    'x-csrf-token': 'contract-token',
  };

  const rejectedOrigin = await fetch(`${baseUrl}/api/owner/v1/login`, {
    method: 'POST',
    headers: { ...mutationHeaders, origin: 'http://evil.local' },
    body: JSON.stringify({ username: 'owner', password: 'secret' }),
  });
  assert.equal(rejectedOrigin.status, 403);
  assert.deepEqual(await rejectedOrigin.json(), { code: 'csrf_rejected' });

  const rejectedCsrf = await fetch(`${baseUrl}/api/owner/v1/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', cookie: csrfCookie, origin: allowedOrigin },
    body: JSON.stringify({ username: 'owner', password: 'secret' }),
  });
  assert.equal(rejectedCsrf.status, 403);
  assert.deepEqual(await rejectedCsrf.json(), { code: 'csrf_rejected' });

  const invalidLogin = await fetch(`${baseUrl}/api/owner/v1/login`, {
    method: 'POST',
    headers: mutationHeaders,
    body: JSON.stringify({ username: 'owner', password: 'wrong' }),
  });
  assert.equal(invalidLogin.status, 401);
  assert.deepEqual(await invalidLogin.json(), { code: 'authentication_failed' });

  const login = await fetch(`${baseUrl}/api/owner/v1/login`, {
    method: 'POST',
    headers: mutationHeaders,
    body: JSON.stringify({ username: 'owner', password: 'secret' }),
  });
  assert.equal(login.status, 204);
  const sessionCookie = login.headers.get('set-cookie').split(';', 1)[0];
  const sessionHeaders = { cookie: `${csrfCookie}; ${sessionCookie}` };
  const csrfSessionHeaders = { ...sessionHeaders, origin: allowedOrigin, 'x-csrf-token': 'contract-token' };

  const status = await fetch(`${baseUrl}/api/owner/v1/auth-status`, { headers: sessionHeaders });
  assert.equal(status.status, 200);
  assert.equal((await status.json()).username, 'owner');

  const refresh = await fetch(`${baseUrl}/api/owner/v1/session/refresh`, {
    method: 'POST',
    headers: csrfSessionHeaders,
  });
  assert.equal(refresh.status, 204);

  authenticated = false;
  const expired = await fetch(`${baseUrl}/api/owner/v1/auth-status`, { headers: sessionHeaders });
  assert.equal(expired.status, 401);

  authenticated = true;
  const logout = await fetch(`${baseUrl}/api/owner/v1/logout`, {
    method: 'POST',
    headers: csrfSessionHeaders,
  });
  assert.equal(logout.status, 204);
  authenticated = false;
});
