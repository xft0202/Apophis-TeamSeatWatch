// Matches auth.TOTPCode's normalization and nonempty base32 decoding.
export function validTwoFactorSecret(value: string) {
  const normalized = value.trim().toUpperCase().replaceAll(' ', '').replace(/=+$/, '');
  return /^[A-Z2-7]+$/.test(normalized) && [0, 2, 4, 5, 7].includes(normalized.length % 8);
}
