import { useActionFeedback } from '../shared/useActionFeedback';
import { useEffect, useState } from 'react';
import { batchOperationsApi, type BatchList } from './batchOperations';

export default function useOperationRecords(active: boolean) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [records, setRecords] = useState<BatchList | null>(null);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [readFailed, setReadFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);

  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    setLoading(true); setReadFailed(false); setNotice('');
    void batchOperationsApi.list(undefined, page, pageSize, controller.signal, true).then((value) => {
      if (controller.signal.aborted) return;
      const lastPage = Math.max(1, Math.ceil(value.total / pageSize));
      if (page > lastPage) { setPage(lastPage); return; }
      setRecords(value);
    }).catch(() => { if (!controller.signal.aborted) { setRecords(null); setReadFailed(true); setNotice('操作记录读取失败，请刷新重试。'); } })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, page, pageSize, revision]);

  const running = Boolean(records?.items.some((item) => item.execution.activeTaskCount > 0));
  useEffect(() => {
    if (!active || loading || !running) return;
    const timer = window.setTimeout(() => setRevision((value) => value + 1), 3000);
    return () => window.clearTimeout(timer);
  }, [active, loading, running, revision]);

  return { page, pageSize, records, notice, dismissNotice, loading, readFailed, setPage,
    setPageSize: (value: number) => { setPageSize(value); setPage(1); },
    refresh: () => setRevision((value) => value + 1) };
}
