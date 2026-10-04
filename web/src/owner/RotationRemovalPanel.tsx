import { Alert, Badge, Button, Group, Pagination, Paper, Select, Stack, Table, Text, Title } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { OwnerApiError } from './auth';
import { expiryRotationApi, type ExpiryPreview } from './expiryRotation';
import { rotationRemovalApi, type Removal, type RemovalHistory, type RemovalSlot } from './rotationRemoval';
import { removalErrorMessage, removalSlotAction, removalSlotStatus } from './rotationRemovalState';
import RotationJoinPanel from './RotationJoinPanel';

// Reads restore persisted work. No effect, poll, response or browser restart can
// start a campaign or dispatch a slot: all mutations require a labelled click.
export default function RotationRemovalPanel({ preview }: { preview: ExpiryPreview | null }) {
  const [selected, setSelected] = useState(preview?.authorizationDigest ? preview.id : '');
  const selectedRef = useRef(selected);
  const generation = useRef(0);
  const historyGeneration = useRef(0);
  const [history, setHistory] = useState<RemovalHistory>({ items: [], page: 1, total: 0 });
  const [page, setPage] = useState(1);
  const [scope, setScope] = useState<ExpiryPreview | null>(preview);
  const [progress, setProgress] = useState<Removal | null>(null);
  const [busySlots, setBusySlots] = useState(new Set<string>());
  const [controlBusy, setControlBusy] = useState(false);
  const [error, setError] = useState('');
  const [now, setNow] = useState(Date.now());

  async function loadHistory(targetPage: number) {
    const token = ++historyGeneration.current;
    try {
      const response = await rotationRemovalApi.history(targetPage);
      if (token !== historyGeneration.current) return;
      setHistory(response);
      const first = response.items[0];
      if (!selectedRef.current && first) {
        selectedRef.current = first.previewId;
        setSelected(first.previewId);
      }
    } catch (cause) { if (token === historyGeneration.current) setError(removalErrorMessage(cause instanceof OwnerApiError ? cause.status : undefined)); }
  }
  async function load(id: string) {
    if (!id || selectedRef.current !== id) return;
    const token = ++generation.current;
    try {
      const [savedScope, savedProgress] = await Promise.all([
        expiryRotationApi.get(id),
        rotationRemovalApi.get(id).catch((cause: unknown) => { if (cause instanceof OwnerApiError && cause.status === 404) return null; throw cause; }),
      ]);
      if (selectedRef.current === id && token === generation.current) { setScope(savedScope); setProgress(savedProgress); setNow(Date.now()); }
    } catch (cause) { if (selectedRef.current === id && token === generation.current) setError(removalErrorMessage(cause instanceof OwnerApiError ? cause.status : undefined)); }
  }
  useEffect(() => {
    if (!preview?.authorizationDigest) return;
    selectedRef.current = preview.id; generation.current++;
    setSelected(preview.id); setScope(preview); setProgress(null);
  }, [preview?.id, preview?.authorizationDigest, preview?.status]); // full server scope is fetched below
  useEffect(() => { void loadHistory(page); return () => { historyGeneration.current++; }; }, [page]);
  useEffect(() => {
    selectedRef.current = selected; generation.current++;
    setProgress(null); setError('');
    void load(selected);
    const timer = window.setInterval(() => { setNow(Date.now()); void load(selected); }, 3000);
    return () => { window.clearInterval(timer); generation.current++; };
  }, [selected]);

  async function control(action: 'start' | 'stop') {
    if (!scope || !selected || controlBusy) return;
    const id = selected;
    setControlBusy(true); setError(''); generation.current++;
    try {
      if (action === 'start') await rotationRemovalApi.start(scope); else await rotationRemovalApi.stop(id);
    } catch (cause) { if (selectedRef.current === id) setError(removalErrorMessage(cause instanceof OwnerApiError ? cause.status : undefined)); }
    finally { await load(id); void loadHistory(page); setControlBusy(false); }
  }
  async function act(slot: RemovalSlot, action: 'run' | 'verify') {
    const id = selected;
    if (!id || busySlots.has(slot.id)) return;
    setBusySlots((current) => new Set(current).add(slot.id)); setError(''); generation.current++;
    try { await rotationRemovalApi[action](id, slot.id); }
    catch (cause) { if (selectedRef.current === id) setError(removalErrorMessage(cause instanceof OwnerApiError ? cause.status : undefined)); }
    finally { await load(id); setBusySlots((current) => { const next = new Set(current); next.delete(slot.id); return next; }); }
  }
  const options = history.items.map((item) => ({ value: item.previewId, label: `${item.workspaceName} · ${new Date(item.createdAt).toLocaleString()}${item.stopped ? ' · 已停止' : ''}` }));
  if (preview?.authorizationDigest && !options.some((item) => item.value === preview.id)) options.unshift({ value: preview.id, label: '本轮已确认范围' });
  if (progress && !options.some((item) => item.value === progress.previewId)) options.unshift({ value: progress.previewId, label: `${progress.workspaceName} · ${new Date(progress.createdAt).toLocaleString()}` });
  const canStart = Boolean(scope?.authorizationDigest && scope.id === selected && scope.status === 'authorized' && Date.parse(scope.expiresAt) > now && !progress);
  const candidates = new Map(scope?.candidates.map((item) => [item.accountId, item.identifier]) ?? []);

  return <Paper withBorder p="md"><Stack gap="sm">
    <Group justify="space-between"><Title order={3}>清退进度</Title><Button size="xs" variant="subtle" onClick={() => { void load(selected); void loadHistory(page); }}>刷新进度</Button></Group>
    {options.length ? <Select label="本轮 / 历史清退" value={selected || null} data={options} onChange={(id) => { generation.current++; selectedRef.current = id || ''; setSelected(id || ''); }} allowDeselect={false} /> : <Text size="sm" c="dimmed">尚无已确认的清退范围</Text>}
    {history.total > 20 ? <Pagination total={Math.ceil(history.total / 20)} value={page} onChange={setPage} /> : null}
    {error ? <Alert color="red" role="alert">{error}</Alert> : null}
    {canStart ? <Button loading={controlBusy} onClick={() => void control('start')}>建立逐席清退进度（尚不清退）</Button> : null}
    {progress ? <>
      <Group justify="space-between"><Group gap="xs"><Text fw={600}>{progress.workspaceName}</Text><Badge color={progress.stopped || !progress.writeAllowed ? 'gray' : 'blue'}>{progress.stopped || !progress.writeAllowed ? '仅可核实原槽' : '已确认范围'}</Badge></Group>
        {!progress.stopped ? <Button variant="light" color="red" loading={controlBusy} onClick={() => void control('stop')}>停止后续清退</Button> : null}</Group>
      <Table.ScrollContainer minWidth={620}><Table withTableBorder striped>
        <Table.Thead><Table.Tr><Table.Th>原成员</Table.Th><Table.Th>原槽状态</Table.Th><Table.Th>固定候选</Table.Th><Table.Th>下一步</Table.Th></Table.Tr></Table.Thead>
        <Table.Tbody>{progress.slots.map((slot) => {
          const action = removalSlotAction(progress, slot, now);
          return <Table.Tr key={slot.id}><Table.Td><Text size="sm">{slot.identifier}</Text><Text size="xs" c="dimmed">{slot.seatType}</Text></Table.Td>
            <Table.Td><Badge color={slot.candidateReady ? 'green' : slot.uncertainObligation ? 'yellow' : 'gray'}>{removalSlotStatus(slot)}</Badge>{slot.uncertainObligation ? <Text size="xs" c="dimmed">原席义务仍保留</Text> : null}</Table.Td>
            <Table.Td><Text size="sm">{candidates.get(slot.candidateAccountId) || '原授权中的固定候选'}</Text><Text size="xs" c="dimmed">{slot.candidateReady ? '空位已核实，候选加入尚未启用' : '不可加入'}</Text></Table.Td>
            <Table.Td>{action ? <Button size="xs" variant="light" disabled={controlBusy} loading={busySlots.has(slot.id)} onClick={() => void act(slot, action)}>{action === 'run' ? '清退此原成员' : '只读核实原槽'}</Button> : <Text size="xs" c="dimmed">{slot.leaseExpiresAt && Date.parse(slot.leaseExpiresAt) > now ? '处理中，可停止后续动作' : '保留当前状态'}</Text>}</Table.Td></Table.Tr>;
        })}</Table.Tbody>
      </Table></Table.ScrollContainer>
    </> : null}
    {progress ? <RotationJoinPanel removal={progress} /> : null}
  </Stack></Paper>;
}
