import { createTheme, type ButtonProps, type MantineTheme } from '@mantine/core';

const indigo = [
  '#eef2ff',
  '#e0e7ff',
  '#c7d2fe',
  '#a5b4fc',
  '#818cf8',
  '#4c4eda',
  '#4042c4',
  '#4338ca',
  '#3730a3',
  '#312e81',
] as const;

const error = ['#fef2f2', '#fee2e2', '#fecaca', '#fca5a5', '#f87171', '#ef4444', '#dc2626', '#b91c1c', '#991b1b', '#7f1d1d'] as const;
const success = ['#ecfdf5', '#d1fae5', '#a7f3d0', '#6ee7b7', '#34d399', '#10b981', '#059669', '#047857', '#065f46', '#064e3b'] as const;
const warning = ['#fffbeb', '#fef3c7', '#fde68a', '#fcd34d', '#fbbf24', '#f59e0b', '#d97706', '#b45309', '#92400e', '#78350f'] as const;

export const appTheme = createTheme({
  primaryColor: 'indigo',
  primaryShade: { light: 5, dark: 6 },
  colors: { indigo, error, success, warning, green: success, red: error, yellow: warning, blue: indigo },
  defaultRadius: '6px',
  black: '#0a0a0a',
  white: '#ffffff',
  fontFamily: '"DM Sans", "Segoe UI", sans-serif',
  fontFamilyMonospace: '"JetBrains Mono", ui-monospace, monospace',
  headings: {
    fontFamily: '"General Sans", "DM Sans", "Segoe UI", sans-serif',
    fontWeight: '600',
    sizes: {
      h1: { fontSize: '32px', lineHeight: '1.25' },
      h2: { fontSize: '24px', lineHeight: '1.3' },
      h3: { fontSize: '24px', lineHeight: '1.3' },
      h4: { fontSize: '15px', lineHeight: '1.5' },
    },
  },
  fontSizes: { xs: '12px', sm: '13px', md: '15px', lg: '24px', xl: '32px' },
  spacing: { xs: '8px', sm: '12px', md: '16px', lg: '24px', xl: '32px' },
  components: {
    Button: {
      defaultProps: { size: 'md', radius: 6 },
      vars: (_theme: MantineTheme, props: ButtonProps) => ({ root: {
        '--button-height': props.size === 'xs' || props.size === 'sm' ? '32px' : props.size === 'lg' || props.size === 'xl' ? '44px' : '38px',
        '--button-fz': '14px',
        '--button-radius': '6px',
      } }),
    },
    Input: { defaultProps: { radius: 6 }, styles: { input: { fontSize: '14px' } } },
    Checkbox: { defaultProps: { radius: 'xl', size: 'md' } },
    Badge: { defaultProps: { radius: 'xl', variant: 'light' } },
    Table: { defaultProps: { highlightOnHover: true, horizontalSpacing: 16, verticalSpacing: 12 } },
    Paper: { defaultProps: { radius: 12 } },
    NavLink: { defaultProps: { variant: 'light' }, styles: { root: { borderRadius: 6 }, label: { fontSize: 14, fontWeight: 600 } } },
  },
  focusRing: 'auto',
});
