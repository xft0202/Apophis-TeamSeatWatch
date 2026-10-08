import { useActionFeedback } from '../shared/useActionFeedback';
import type { StatusTone } from '../shared/StatusBadge';
import { useEffect, useRef, useState } from 'react';
import type { components } from '../generated/owner';
import { exchangeSelectedWorkspaceToken, getSelectedWorkspaceAccess, getSelectedWorkspaceVerification, ownerProblem, verifySelectedWorkspace } from './auth';
import { canShowSelectedWorkspaceFacts, createWorkspaceRequestGate } from './workspaceVerification';

type Access = components['schemas']['SelectedWorkspaceAccessStatus'];
type Facts = components['schemas']['SelectedWorkspaceVerification'];
export type WorkspaceEntryKind = 'member' | 'pending_invite';

export function useSelectedWorkspace(motherId: string, workspaceId: string) {
  const [access, setAccess] = useState<Access | null>(null);
  const [facts, setFacts] = useState<Facts | null>(null);
  const [kind, setKind] = useState<WorkspaceEntryKind>('member');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [noticeTone, setNoticeTone] = useState<StatusTone>('error');
  const [now, setNow] = useState(Date.now());
  const gate = useRef(createWorkspaceRequestGate());
  const polling = useRef<AbortController | null>(null);

  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 30_000); return () => window.clearInterval(timer); }, []);
  useEffect(() => {
    const load = () => {
      const sequence = gate.current.beginPoll(); if (sequence === null) return;
      polling.current?.abort(); const controller = new AbortController(); polling.current = controller;
      setLoading(true);
      void Promise.all([
        getSelectedWorkspaceAccess(workspaceId, motherId, controller.signal),
        getSelectedWorkspaceVerification(workspaceId, motherId, controller.signal, { page, pageSize, kind }),
      ]).then(([nextAccess, nextFacts]) => {
        if (controller.signal.aborted || !gate.current.acceptPoll(sequence)) return;
        setAccess(nextAccess); setFacts(nextFacts); setNotice('');
      }).catch(() => {
        if (controller.signal.aborted || !gate.current.acceptPoll(sequence)) return;
        setFacts(null); setNoticeTone('error'); setNotice('空间信息读取失败，请重试。');
      }).finally(() => { if (!controller.signal.aborted && gate.current.acceptPoll(sequence)) setLoading(false); });
    };
    load(); const timer = window.setInterval(load, 60_000);
    return () => { polling.current?.abort(); gate.current.invalidate(); window.clearInterval(timer); };
  }, [motherId, workspaceId, page, pageSize, kind]);

  async function sync() {
    if (pending) return;
    const sequence = gate.current.beginMutation(); polling.current?.abort(); setLoading(false); setPending(true); setNotice('');
    try {
      const validAccess = access?.status === 'ready' && access.expiresAt && Date.parse(access.expiresAt) > Date.now();
      const nextAccess = validAccess ? access : await exchangeSelectedWorkspaceToken(workspaceId, motherId);
      if (!gate.current.isCurrentMutation(sequence)) return;
      setAccess(nextAccess);
      if (nextAccess?.status !== 'ready') { setFacts(null); setNoticeTone('error'); setNotice('空间连接失败，请重试。'); return; }
      await verifySelectedWorkspace(workspaceId, motherId);
      const next = await getSelectedWorkspaceVerification(workspaceId, motherId, undefined, { page, pageSize, kind });
      if (!gate.current.isCurrentMutation(sequence)) return;
      setFacts(next); setNow(Date.now());
      setNoticeTone(next.status === 'verified' ? 'success' : next.status === 'permission_denied' ? 'error' : 'warning');
      setNotice(next.status === 'verified' ? '空间信息已同步。' : next.status === 'permission_denied' ? '当前母号没有读取此空间的权限。' : '部分信息未读取成功，请重试。');
    } catch (error: unknown) {
      if (!gate.current.isCurrentMutation(sequence)) return;
      const problem = ownerProblem(error); setFacts(null);
      if (problem.code === 'workspace_token_required') setAccess(null);
      setNoticeTone('error'); setNotice(problem.status === 401 ? '登录已失效，请重新登录。' : problem.status === 409 ? '空间入口已变化，请重新发现空间。' : '空间同步失败，请重试。');
    } finally { if (gate.current.acceptMutation(sequence)) setPending(false); }
  }
  return { access, facts, loading, pending, notice, noticeTone, dismissNotice, sync, page, pageSize, kind, now,
    ready: canShowSelectedWorkspaceFacts(facts, access, now), setPage,
    setPageSize: (value: number) => { setPageSize(value); setPage(1); },
    setKind: (value: WorkspaceEntryKind) => { setKind(value); setPage(1); },
  };
}
