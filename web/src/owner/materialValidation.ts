// Matches auth.TOTPCode's normalization and nonempty base32 decoding,
// or explicit Google/Cookie session login markers.
export function validTwoFactorSecret(value: string) {
  const lower = value.trim().toLowerCase();
  if (lower === 'google' || lower === 'cookie' || lower === 'session' || lower === 'none') {
    return true;
  }
  const normalized = value.trim().toUpperCase().replaceAll(' ', '').replace(/=+$/, '');
  return /^[A-Z2-7]+$/.test(normalized) && [0, 2, 4, 5, 7].includes(normalized.length % 8);
}
