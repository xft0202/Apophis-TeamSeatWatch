import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { businessTimeZone, dateBoundary, formatCalendarDate, formatDateTime, formatDateTimeInput, parseDateTimeInput } from '../src/shared/dateTime.ts';

test('all business timestamps use Shanghai, including UTC midnight crossings', () => {
  assert.equal(businessTimeZone, 'Asia/Shanghai');
  assert.equal(formatDateTime('2026-10-07T20:05:06Z').replaceAll('\u00a0', ' '), '2026/10/8 04:05:06');
  assert.equal(formatDateTime('2026-10-07T16:00:00Z').replaceAll('\u00a0', ' '), '2026/10/8 00:00:00');
  assert.equal(formatDateTime('2026-10-07T20:05:06Z', false).replaceAll('\u00a0', ' '), '2026/10/8 04:05');
  assert.equal(formatCalendarDate('2026-10-07T20:05:06Z'), '2026-10-08');
  assert.equal(formatDateTime('invalid'), '—');
});

test('scheduled-time editing and submission preserve the same Shanghai instant', () => {
  const timestamp = '2026-10-07T20:05:06Z';
  assert.equal(formatDateTimeInput(timestamp), '2026-10-08T04:05');
  assert.equal(parseDateTimeInput('2026-10-08T04:05').toISOString(), '2026-10-07T20:05:00.000Z');
  assert.equal(parseDateTimeInput('2026-10-08T04:05:06').toISOString(), timestamp.replace('Z', '.000Z'));
  assert.equal(parseDateTimeInput('2026-10-08T00:00').toISOString(), '2026-10-07T16:00:00.000Z');
  for (const value of ['', '2026-02-30T12:00', '2026-10-08T24:00', '2026-10-08T12:60', '2026-10-08T12:00Z']) assert.equal(parseDateTimeInput(value), null);
});

test('redemption date ranges use inclusive Shanghai days and an exclusive next-day boundary', () => {
  assert.equal(dateBoundary('2026-10-08'), '2026-10-07T16:00:00.000Z');
  assert.equal(dateBoundary('2026-10-08', true), '2026-10-08T16:00:00.000Z');
  assert.equal(dateBoundary('2028-02-29', true), '2028-02-29T16:00:00.000Z');
  assert.equal(dateBoundary('2026-12-31', true), '2026-12-31T16:00:00.000Z');
  for (const value of ['', '2026-02-30', '2026-13-01', 'invalid']) assert.equal(dateBoundary(value), undefined);
});

test('device timezone cannot change display, schedule submission or redemption filters', () => {
  const module = new URL('../src/shared/dateTime.ts', import.meta.url).href;
  const source = `const time = await import(${JSON.stringify(module)}); process.stdout.write(JSON.stringify([time.formatDateTime('2026-10-07T20:05:06Z'),time.parseDateTimeInput('2026-10-08T04:05').toISOString(),time.dateBoundary('2026-10-08'),time.formatCalendarDate('2026-10-07T20:05:06Z')]));`;
  const results = ['UTC', 'America/New_York', 'Asia/Shanghai'].map(TZ => {
    const result = spawnSync(process.execPath, ['--experimental-strip-types', '--input-type=module', '-e', source], { env: { ...process.env, TZ }, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    return JSON.parse(result.stdout);
  });
  assert.deepEqual(results[0], results[1]);
  assert.deepEqual(results[1], results[2]);
});
