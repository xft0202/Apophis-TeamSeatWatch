import { Alert, type AlertProps } from '@mantine/core';
import type { StatusTone } from './StatusBadge';

type Props = Omit<AlertProps, 'children' | 'color' | 'onClose' | 'role'> & {
  message: string;
  tone?: StatusTone;
  onClose: () => void;
};

export default function ActionNotice({ message, tone = 'error', onClose, ...props }: Props) {
  if (!message) return null;
  return <Alert {...props} color={tone} role={tone === 'error' ? 'alert' : 'status'} withCloseButton closeButtonLabel="关闭提示" onClose={onClose}>{message}</Alert>;
}
