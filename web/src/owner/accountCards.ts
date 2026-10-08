import { activateMembershipCard, generateAccountCard } from './deliveryRecords';

type CardIntent = { secret: string; request: string; saved: boolean };
const keyFor = (id: string) => `owner-account-card:${id}`;
export function accountCardIntent(id: string): CardIntent | null {
  try {
    const raw = sessionStorage.getItem(keyFor(id));
    if (!raw) return null;
    const value: unknown = JSON.parse(raw);
    if (typeof value !== 'object' || value === null || !('secret' in value) || !('request' in value) || !('saved' in value) || typeof value.secret !== 'string' || typeof value.request !== 'string' || typeof value.saved !== 'boolean') return null;
    return { secret: value.secret, request: value.request, saved: value.saved };
  } catch { return null; }
}

// Save the original secret and request before sending. A lost response retries
// the same activation rather than minting an unusable second card.
export async function saveAccountCard(id: string) {
  const intent = accountCardIntent(id) ?? { secret: generateAccountCard(), request: crypto.randomUUID(), saved: false };
  sessionStorage.setItem(keyFor(id), JSON.stringify(intent));
  const activation = await activateMembershipCard(id, intent.secret, intent.request);
  sessionStorage.setItem(keyFor(id), JSON.stringify({ ...intent, saved: true }));
  return { activation, secret: intent.secret };
}
