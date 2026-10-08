import { useActionFeedback } from '../shared/useActionFeedback';
import { useDocumentVisibility } from '@mantine/hooks';
import { useEffect, useRef, useState } from 'react';
import { ownerProblem } from './auth';
import { batchRotationApi, type RemovalOperation, type RemovalPreview } from './batchRotation';
import { createRotationJoinRequests } from './rotationJoinRequests';
import { acknowledgeOperationIntent, operationIntent } from './operationIntent';
import useTaskConcurrency from './useTaskConcurrency';

export function useBatchRemoval(batchId: string, active: boolean) {
  const taskConcurrency = useTaskConcurrency(active);
  const [preview, setPreview] = useState<RemovalPreview | null>(null);
  const [operation, setOperation] = useState<RemovalOperation | null>(null);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [loading, setLoading] = useState(false);
  const [pending, setPending] = useState(false);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [revision, setRevision] = useState(0);
  const gate = useRef(createRotationJoinRequests()).current;
  const read = useRef<AbortController | null>(null);
  const visible = useDocumentVisibility() === 'visible';
  const currentPreview = preview?.batch.id === batchId ? preview : null;
  const currentOperation = operation?.batchId === batchId ? operation : null;

  useEffect(() => {
    gate.invalidate(); read.current?.abort(); setPreview(null); setOperation(null); setNotice(''); setPending(false); setLoading(false); setPage(1); setPageSize(20);
    return () => { gate.invalidate(); read.current?.abort(); };
  }, [batchId, active]);
  useEffect(() => {
    if (!batchId || !active || !visible) return;
    const controller = new AbortController(); read.current = controller;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function refresh() {
      const token = gate.beginRead(); if (!token) return;
      setLoading(true);
      try {
        const [scope, progress] = await Promise.all([
          batchRotationApi.preview(batchId, page, pageSize, controller.signal),
          batchRotationApi.operation(batchId, page, pageSize, controller.signal).catch((error: unknown) => {
            if (ownerProblem(error).status === 404) return null;
            throw error;
          }),
        ]);
        if (controller.signal.aborted || !gate.isCurrentRead(token)) return;
        setPreview(scope); setOperation(progress); setNotice('');
        const last = Math.max(1, Math.ceil((progress?.targetTotal ?? scope.targetTotal) / pageSize));
        if (page > last) { setPage(last); return; }
        if (progress && ['queued', 'running'].includes(progress.status)) timer = setTimeout(() => void refresh(), 3000);
      } catch (error: unknown) {
        if (!controller.signal.aborted && gate.isCurrentRead(token)) {
          setPreview(null); setOperation(null);
          setNotice(ownerProblem(error).status === 401 ? '登录已失效，请重新登录。' : '轮转详情读取失败，请刷新重试。');
        }
      } finally { if (!controller.signal.aborted && gate.isCurrentRead(token)) setLoading(false); }
    }
    void refresh();
    return () => { controller.abort(); if (timer) clearTimeout(timer); };
  }, [batchId, active, visible, page, pageSize, revision]);

  async function act(action: 'start' | 'reconcile') {
    if (!batchId || !active || loading || (action === 'start' && (!currentPreview?.canProceed || currentOperation))) return;
    if (action === 'reconcile' && (!currentOperation || !['failed', 'blocked'].includes(currentOperation.status))) return;
    const token = gate.beginAction('removal'); if (!token) return;
    read.current?.abort(); setPending(true); setNotice('');
    const intent = `remove:${batchId}:${action}`;
    try {
      if (action === 'start') await batchRotationApi.start(batchId, `remove:${batchId}`, taskConcurrency.concurrency);
      else await batchRotationApi.reconcile(batchId, operationIntent(intent));
      acknowledgeOperationIntent(intent);
    } catch (error: unknown) {
      if (gate.isCurrentAction(token)) setNotice(ownerProblem(error).status === 409 ? '本轮状态已变化，请刷新后核对。' : '请求结果待核验，请刷新原轮次继续。');
    } finally {
      if (gate.finishAction(token)) { setPreview(null); setOperation(null); setPending(false); setLoading(false); setRevision((value) => value + 1); }
    }
  }
  return { taskConcurrency, preview: currentPreview, operation: currentOperation, page, pageSize, loading, pending, notice, dismissNotice, act,
    refresh: () => setRevision((value) => value + 1), setPage,
    setPageSize: (value: number) => { setPageSize(value); setPage(1); },
  };
}
