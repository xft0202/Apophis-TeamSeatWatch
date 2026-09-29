import { createTheme } from '@mantine/core';

const indigo = [
  '#eef2ff',
  '#e0e7ff',
  '#c7d2fe',
  '#a5b4fc',
  '#818cf8',
  '#6366f1',
  '#4f46e5',
  '#4338ca',
  '#3730a3',
  '#312e81',
] as const;

const error = ['#fef2f2', '#fee2e2', '#fecaca', '#fca5a5', '#f87171', '#ef4444', '#dc2626', '#b91c1c', '#991b1b', '#7f1d1d'] as const;
const success = ['#ecfdf5', '#d1fae5', '#a7f3d0', '#6ee7b7', '#34d399', '#10b981', '#059669', '#047857', '#065f46', '#064e3b'] as const;

export const appTheme = createTheme({
  primaryColor: 'indigo',
  primaryShade: { light: 5, dark: 6 },
  colors: { indigo, error, success },
  defaultRadius: '6px',
  fontFamily: '"DM Sans", "Segoe UI", sans-serif',
  fontFamilyMonospace: '"JetBrains Mono", ui-monospace, monospace',
  headings: {
    fontFamily: '"General Sans", "DM Sans", "Segoe UI", sans-serif',
    fontWeight: '600',
  },
  focusRing: 'auto',
});
