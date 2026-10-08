import { useActionFeedback, ACTION_FEEDBACK_MS } from './useActionFeedback';
import { useEffect, useRef, useState } from 'react';

export function useRecordCopy() {
  const [copyingId, setCopyingId] = useState<string | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const locked = useRef(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  async function copy(id: string, read: () => Promise<string>) {
    if (locked.current) return;
    locked.current = true; setCopyingId(id); setCopiedId(null); setNotice('');
    try {
      await navigator.clipboard.writeText(await read());
      setCopiedId(id);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setCopiedId(null), ACTION_FEEDBACK_MS);
    } catch { setNotice('复制失败，请重试'); }
    finally { locked.current = false; setCopyingId(null); }
  }

  return { copy, copyingId, copiedId, notice, dismissNotice };
}
