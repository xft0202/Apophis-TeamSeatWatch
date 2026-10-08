// A displayed ready session must stop looking usable at its expiry even if
// the Owner keeps this panel open without making another API request.
export function visiblePersonalStatus(status: string | undefined, expiresAt: string | null | undefined, now = Date.now()): string {
  if (status === 'ready') {
    const expiry = expiresAt ? Date.parse(expiresAt) : NaN;
    if (!Number.isFinite(expiry) || expiry <= now) return 'session_expired';
  }
  return status ?? '读取中';
}
