// Preserve the same POST identity until the server acknowledges the request.
const prefix = 'owner-operation-intent:';
const fallback = new Map<string, string>();
export function operationIntent(scope: string): string {
  const key = prefix + scope;
  let value = fallback.get(key);
  try { value = sessionStorage.getItem(key) ?? value; } catch { /* Memory still prevents double dispatch in this tab. */ }
  if (!value) value = crypto.randomUUID();
  fallback.set(key, value);
  try { sessionStorage.setItem(key, value); } catch { /* Browser storage may be unavailable. */ }
  return value;
}
export function acknowledgeOperationIntent(scope: string) {
  const key = prefix + scope;
  fallback.delete(key);
  try { sessionStorage.removeItem(key); } catch { /* Ignore unavailable browser storage. */ }
}
