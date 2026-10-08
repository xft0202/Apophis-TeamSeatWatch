export const businessTimeZone = 'Asia/Shanghai';

const dateTimeOptions: Intl.DateTimeFormatOptions = {
  timeZone: businessTimeZone,
  year: 'numeric', month: 'numeric', day: 'numeric',
  hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
};
const dateTime = new Intl.DateTimeFormat('zh-CN', { ...dateTimeOptions, second: '2-digit' });
const dateTimeMinutes = new Intl.DateTimeFormat('zh-CN', dateTimeOptions);
const inputDateTime = new Intl.DateTimeFormat('en-CA', {
  timeZone: businessTimeZone, year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
});

export function formatDateTime(value: string, seconds = true): string {
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? (seconds ? dateTime : dateTimeMinutes).format(date).replaceAll(' ', '\u00a0') : '—';
}

export function formatDateTimeInput(value: string): string {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  const parts = Object.fromEntries(inputDateTime.formatToParts(date).map(part => [part.type, part.value]));
  return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
}

export function parseDateTimeInput(value: string): Date | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?$/.test(value)) return null;
  // Business date controls use Shanghai's UTC+08:00, independent of the device.
  const date = new Date(`${value}${value.length === 16 ? ':00' : ''}+08:00`);
  if (!Number.isFinite(date.getTime()) || formatDateTimeInput(date.toISOString()) !== value.slice(0, 16)) return null;
  return date;
}

export function dateBoundary(value: string, nextDay = false): string | undefined {
  const date = parseDateTimeInput(`${value}T00:00`);
  return date ? new Date(date.getTime() + (nextDay ? 86400000 : 0)).toISOString() : undefined;
}

export function formatCalendarDate(value: string): string {
  return formatDateTimeInput(value).slice(0, 10);
}
