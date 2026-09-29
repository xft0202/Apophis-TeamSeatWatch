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

export const appTheme = createTheme({
  primaryColor: 'indigo',
  primaryShade: { light: 5, dark: 6 },
  colors: { indigo },
  defaultRadius: 'sm',
  fontFamily: '"DM Sans", "Segoe UI", sans-serif',
  fontFamilyMonospace: '"JetBrains Mono", ui-monospace, monospace',
  headings: {
    fontFamily: '"General Sans", "DM Sans", "Segoe UI", sans-serif',
    fontWeight: '700',
  },
  focusRing: 'auto',
});
