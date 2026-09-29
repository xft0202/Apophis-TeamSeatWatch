import createClient from 'openapi-fetch';
import type { components, paths } from '../../generated/owner';

const ownerApi = createClient<paths>({
  baseUrl: '',
  credentials: 'include',
});

type LoginCredentials = components['schemas']['LoginRequest'];
type AuthStatus = components['schemas']['AuthStatus'];

type OwnerApiResponse = {
  status: number;
  body: unknown;
};

export class OwnerApiError extends Error implements OwnerApiResponse {
  readonly status: number;
  readonly body: unknown;

  constructor(status: number, body: unknown) {
    super('owner_api_request_failed');
    this.name = 'OwnerApiError';
    this.status = status;
    this.body = body;
  }
}

let csrfToken = '';
let csrfRequest: Promise<string> | undefined;

async function ensureCsrf(): Promise<string> {
  if (csrfToken) return csrfToken;
  if (!csrfRequest) {
    csrfRequest = (async () => {
      const response = await ownerApi.GET('/api/owner/v1/csrf');
      if (response.error || !response.data) {
        throw new OwnerApiError(response.response.status, response.error);
      }
      csrfToken = response.data.token;
      return csrfToken;
    })().finally(() => {
      csrfRequest = undefined;
    });
  }
  return csrfRequest;
}

export function mutationHeaders() {
  return ensureCsrf().then((token) => ({ 'X-CSRF-Token': token }));
}

function throwIfFailed(response: { error?: unknown; response: Response }): void {
  if (response.error) {
    throw new OwnerApiError(response.response.status, response.error);
  }
}

export async function getAuthStatus(): Promise<AuthStatus> {
  const response = await ownerApi.GET('/api/owner/v1/auth-status');
  throwIfFailed(response);
  if (!response.data) {
    throw new OwnerApiError(response.response.status, undefined);
  }
  return response.data;
}

export async function loginOwner(credentials: LoginCredentials): Promise<void> {
  const response = await ownerApi.POST('/api/owner/v1/login', {
    params: { header: await mutationHeaders() },
    body: credentials,
  });
  throwIfFailed(response);
}

export async function refreshOwnerSession(): Promise<void> {
  const response = await ownerApi.POST('/api/owner/v1/session/refresh', {
    params: { header: await mutationHeaders() },
  });
  throwIfFailed(response);
}

export async function listMotherAccounts(search = ''): Promise<components['schemas']['MotherAccountList']> {
  const query = search ? { page: 1, page_size: 100, search } : { page: 1, page_size: 100 };
  const response = await ownerApi.GET('/api/owner/v1/mother-accounts', {
    params: { query },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function updateMotherAccountMaterial(account: components['schemas']['MotherAccount'], password: string, totpSecret: string): Promise<components['schemas']['MotherAccount']> {
  const response = await ownerApi.PATCH('/api/owner/v1/mother-accounts/{accountId}', {
    params: { path: { accountId: account.id }, header: { ...(await mutationHeaders()), 'If-Match': `"${account.version}"` } },
    body: { displayName: account.displayName, status: account.status, password, totpSecret },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function importMotherAccounts(content: string): Promise<components['schemas']['MotherAccountImportResult']> {
  const response = await ownerApi.POST('/api/owner/v1/mother-accounts/import', {
    params: { header: await mutationHeaders() },
    body: { content },
  });
  throwIfFailed(response);
  if (!response.data) throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function exportMotherAccounts(accountIds: string[] | undefined, expectedCount: number): Promise<string> {
  const body = accountIds ? { accountIds, expectedCount, confirmed: true } : { expectedCount, confirmed: true };
  const response = await ownerApi.POST('/api/owner/v1/mother-accounts/export', {
    params: { header: await mutationHeaders() },
    body,
  });
  throwIfFailed(response);
  if (typeof response.data !== 'string') throw new OwnerApiError(response.response.status, undefined);
  return response.data;
}

export async function logoutOwner(): Promise<void> {
  const response = await ownerApi.POST('/api/owner/v1/logout', {
    params: { header: await mutationHeaders() },
  });
  throwIfFailed(response);
}

export function clearCsrf(): void {
  csrfToken = '';
  csrfRequest = undefined;
}

export function ownerProblem(error: unknown): {
  status: number | undefined;
  code: string | undefined;
  retryAfterSeconds: number | undefined;
} {
  if (!(error instanceof OwnerApiError)) {
    return { status: undefined, code: undefined, retryAfterSeconds: undefined };
  }

  const body = typeof error.body === 'object' && error.body !== null
    ? error.body as { code?: unknown; retryAfterSeconds?: unknown }
    : {};

  return {
    status: error.status,
    code: typeof body.code === 'string' ? body.code : undefined,
    retryAfterSeconds: typeof body.retryAfterSeconds === 'number'
      ? body.retryAfterSeconds
      : undefined,
  };
}
