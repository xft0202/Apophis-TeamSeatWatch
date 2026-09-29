import { Alert, Badge, Button, Checkbox, Group, Paper, ScrollArea, Stack, Table, Text } from '@mantine/core';
import { useEffect, useState } from 'react';
import type { components } from '../../generated/owner';
import { cancelPersonalProbes, createPersonalProbes, getPersonalProbes, getPersonalProbesByRequest, ownerProblem, previewPersonalProbes } from './auth';
import { personalProbeScope } from './personalProbeScope';

type Batch = components['schemas']['PersonalProbeBatch'];
type Preview = components['schemas']['PersonalProbePreview'];
const storageKey = 'owner-personal-probe-batch';
const pendingKeyStorage = 'owner-personal-probe-pending-request';

const outcomeLabels: Record<string, string> = {
  available: '已验证 Personal 用量', missing_personal_credential: '缺 Personal 凭据',
  credential_invalid: '凭据无效 (401)', forbidden: '无权限 (403)',
  banned: '已明确停用', network_error: '网络错误', unknown: '未知',
};

export default function PersonalProbesView({ selected, search, loading }: { selected: ReadonlySet<string>; search: string; loading: boolean }) {
  const [preview, setPreview] = useState<Preview | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [batch, setBatch] = useState<Batch | null>(null);
  const [batchId, setBatchId] = useState(() => sessionStorage.getItem(storageKey));
  const [pendingKey, setPendingKey] = useState(() => sessionStorage.getItem(pendingKeyStorage));
  const [notice, setNotice] = useState('');
  const [disconnected, setDisconnected] = useState(false);
  const [pending, setPending] = useState(false);
  const scope = personalProbeScope(selected, search);
  const selectionKey = selected.size ? `selected:${[...selected].sort().join(',')}` : `filtered:${search}`;
  const saveFailures = batch?.items.filter((item) => item.status === 'running' && item.evidenceCode === 'result_persistence_failed').length ?? 0;
  const staleAttempts = batch?.items.filter((item) => item.status === 'running' && item.startedAt && Date.now() - Date.parse(item.startedAt) > 5 * 60_000).length ?? 0;

  useEffect(() => { setPreview(null); setConfirmed(false); }, [selectionKey]);
  useEffect(() => {
    if (!pendingKey) return;
    let active = true;
    async function recover() {
      try {
        const result = await getPersonalProbesByRequest(pendingKey!);
        if (!active) return;
        sessionStorage.removeItem(pendingKeyStorage);
        sessionStorage.setItem(storageKey, result.id);
        setPendingKey(null); setBatch(result); setBatchId(result.id); setNotice('');
      } catch (error: unknown) {
        if (!active) return;
        setNotice(ownerProblem(error).status === 404
          ? '探测请求尚未确认保存；仍可按请求编号恢复，未收到结果不是正常。'
          : '请求恢复失败或连接中断；未确认任务状态，请勿重复探测。');
      }
    }
    void recover();
    const timer = window.setInterval(() => void recover(), 2000);
    return () => { active = false; window.clearInterval(timer); };
  }, [pendingKey]);
  useEffect(() => {
    if (!batchId) return;
    let active = true;
    async function refresh() {
      try {
        const result = await getPersonalProbes(batchId!);
        if (active) { setBatch(result); setDisconnected(false); setNotice((value) => value.startsWith('连接中断') ? '' : value); }
      } catch (error: unknown) {
        if (active) { setDisconnected(true); setNotice(ownerProblem(error).status === 401 ? '登录已失效，无法读取探测结果。' : '连接中断或结果无法持久读取；不要将未收到的结果视为正常。'); }
      }
    }
    void refresh();
    const timer = window.setInterval(() => void refresh(), 2000);
    return () => { active = false; window.clearInterval(timer); };
  }, [batchId]);

  async function inspect() {
    setPending(true); setNotice(''); setPreview(null); setConfirmed(false);
    try { setPreview(await previewPersonalProbes(scope)); }
    catch (error: unknown) { setNotice(ownerProblem(error).status === 409 ? '账号范围已变化，请刷新后重试。' : '无法确认范围，请勿开始探测。'); }
    finally { setPending(false); }
  }
  async function start() {
    if (!preview || !confirmed || preview.count < 1) return;
    setPending(true); setNotice('');
    try {
      const requestKey = crypto.randomUUID();
      sessionStorage.setItem(pendingKeyStorage, requestKey);
      setPendingKey(requestKey);
      const result = await createPersonalProbes(scope, preview.count, requestKey);
      sessionStorage.removeItem(pendingKeyStorage);
      setPendingKey(null);
      sessionStorage.setItem(storageKey, result.id);
      setBatch(result); setBatchId(result.id); setPreview(null); setConfirmed(false);
    } catch (error: unknown) {
      if (ownerProblem(error).status === 409) {
        sessionStorage.removeItem(pendingKeyStorage); setPendingKey(null);
      }
      setNotice(ownerProblem(error).status === 409 ? '账号数量或范围已变化，请重新确认。' : '创建响应未收到或保存失败，正在按请求编号核对；不能声称已启动。');
      setPreview(null); setConfirmed(false);
    } finally { setPending(false); }
  }
  async function cancel() {
    if (!batch) return;
    setPending(true); setNotice('');
    try { setBatch(await cancelPersonalProbes(batch.id)); }
    catch { setNotice('取消未确认保存，任务可能仍在运行，请重试读取。'); }
    finally { setPending(false); }
  }

  return <Paper withBorder radius={12} p="xl"><Stack gap="md">
    <Text fw={600}>账号级 Personal 探测</Text>
    <Text size="sm" c="dimmed">仅使用已有 Personal 凭据；不使用密码重新登录。结果不代表空间成员、目标 Workspace 用量或交付资格。</Text>
    {notice ? <Alert color="error">{notice}</Alert> : null}
    <Group><Button variant="light" disabled={loading || pending} onClick={() => void inspect()}>确认探测范围</Button>
      {preview ? <Text size="sm">{preview.label} · 确定 {preview.count} 个账号（不受分页限制）</Text> : null}</Group>
    {preview ? <Group><Checkbox checked={confirmed} disabled={preview.count === 0} onChange={(event) => setConfirmed(event.currentTarget.checked)} label={`确认探测 ${preview.count} 个账号`} />
      <Button disabled={!confirmed || preview.count === 0 || pending || !!pendingKey} loading={pending} onClick={() => void start()}>开始探测</Button></Group> : null}
    {pendingKey ? <Alert color="yellow">正在按请求编号核对上次提交；如持续显示，请检查服务，不能把未知状态视作完成。<Button size="xs" variant="subtle" onClick={() => {
      if (window.confirm('无法确认上次请求是否已执行。放弃本次请求追踪后可能重复探测，确定放弃？')) {
        sessionStorage.removeItem(pendingKeyStorage); setPendingKey(null);
      }
    }}>放弃追踪</Button></Alert> : null}
    {batch ? <Stack gap="sm"><Group justify="space-between"><Text size="sm">{batch.scope === 'selected' ? '跨页已选账号' : batch.label} · 共 {batch.total} · 排队 {batch.queued} · 处理中 {batch.running} · 已保存 {batch.succeeded + batch.failed} · 失败 {batch.failed} · 已取消 {batch.canceled} · 未保存或已留存清除 {batch.notSavedOrRetained}</Text>
      <Button variant="outline" color="red" disabled={pending || batch.queued + batch.running === 0} onClick={() => void cancel()}>取消待处理</Button></Group>
      {disconnected ? <Alert color="error">断线/读取失败：下方是上次保存的显示快照，不能视为当前进度。</Alert> : null}
      {saveFailures > 0 ? <Alert color="error">{saveFailures} 个账号结果保存失败，等待安全重试；不能视为正常。</Alert> : null}
      {staleAttempts > 0 ? <Alert color="yellow">{staleAttempts} 个处理中尝试超时或保存未确认，等待重试；不能视为完成。</Alert> : null}
      {batch.queued + batch.running + batch.notSavedOrRetained > 0 ? <Alert color="yellow">仍有 {batch.queued + batch.running + batch.notSavedOrRetained} 个账号未收到持久结果或已被留存清除；不可视为完成。</Alert> : null}
      {batch.failed > 0 ? <Alert color="yellow">部分探测失败；缺 Personal 凭据不是账号正常。</Alert> : null}
      <ScrollArea h={280}><Table striped><Table.Thead><Table.Tr><Table.Th>账号</Table.Th><Table.Th>进度</Table.Th><Table.Th>保存结果</Table.Th><Table.Th>尝试</Table.Th></Table.Tr></Table.Thead><Table.Tbody>
        {batch.items.map((item) => <Table.Tr key={item.targetAccountId}><Table.Td>{item.identifier}</Table.Td><Table.Td>{item.status}</Table.Td><Table.Td><Badge color={item.outcome === 'available' ? 'green' : item.outcome ? 'yellow' : 'gray'}>{item.outcome ? outcomeLabels[item.outcome] ?? '未知' : '未保存结论'}</Badge></Table.Td><Table.Td>{item.attemptCount}</Table.Td></Table.Tr>)}
      </Table.Tbody></Table></ScrollArea>
    </Stack> : null}
  </Stack></Paper>;
}
