import ActionNotice from '../shared/ActionNotice';
import { useActionFeedback } from '../shared/useActionFeedback';
import { usePageTitle } from '../shared/usePageTitle';
import { Badge, Box, Button, Container, Group, Paper, SegmentedControl, Stack, Text, Textarea, Title } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { publicApi, publicErrorMessage, type Confirmation, type CredentialStatus, type DeliveryState, type RecoveryState } from './api';
import { canDownload, createPublicRequests, MAX_CARDS, parseCards } from './redeemState';

type CardResult = { secret: string; suffix: string; state?: Confirmation | DeliveryState; recovery?: RecoveryState; credential?: CredentialStatus; error?: string; downloadError?: string };
type RecoveryCheck = { credential: CredentialStatus; recovery?: RecoveryState };

function recoveryLabel(value?: RecoveryState): string {
  if (!value) return '未检查';
  if (value.status === 'queued' || value.status === 'running' || value.status === 'retry_wait') return '找回中';
  if (value.result === 'restored' && value.livenessStatus === 'unknown') return '下载已恢复 · 凭据未核验';
  if (value.result === 'healthy') return '凭据正常';
  if (value.deliveryStatus === 'available' && value.livenessStatus === 'healthy') return '已找回';
  if (value.status === 'failed') return '找回失败';
  return '待核验';
}

function credentialLabel(value?: CredentialStatus): string {
  if (!value) return '';
  if (value.status === 'healthy') return '凭据正常';
  if (value.status === 'need_reclaim') return '凭据待找回';
  if (value.status === 'cannot_reclaim') return '凭据不可恢复';
  return '凭据待核验';
}

export default function RedeemPage() {
  const [mode, setMode] = useState<'redeem' | 'recover'>('redeem');
  usePageTitle(mode === 'redeem' ? '卡密兑换' : '401 找回');
  const [input, setInput] = useState('');
  const [results, setResults] = useState<CardResult[]>([]);
  const [error, setError, dismissError] = useActionFeedback('');
  const [busy, setBusy] = useState(false);
  const requests = useRef(createPublicRequests()).current;
  const cards = parseCards(input);

  async function operation(action: (token: number) => Promise<void>) {
    const token = requests.begin();
    if (token === null) return;
    setBusy(true); setError('');
    try { await action(token); }
    catch (cause) { if (requests.current(token)) setError(publicErrorMessage(cause)); }
    finally { if (requests.finish(token)) setBusy(false); }
  }

  useEffect(() => {
    void operation(async (token) => {
      try {
        const state = await publicApi.state();
        if (requests.current(token) && state.hasOrder) setResults([{ secret: '', suffix: state.cardSuffix, state }]);
      } catch { /* A new visitor has no existing order authorization. */ }
    });
    return () => requests.invalidate();
  }, []);

  async function submit() {
    await operation(async (token) => {
      const next: CardResult[] = [];
      for (const secret of cards) {
        if (!requests.current(token)) return;
        try {
          if (mode === 'recover') {
            const checked = await recoverAndPoll(secret, token);
            const state = await checkedDelivery(secret, checked, token);
            next.push({ secret, suffix: state?.cardSuffix ?? secret.slice(-8), ...(checked.recovery ? { recovery: checked.recovery } : {}), credential: checked.credential, ...(state ? { state } : {}) });
          } else {
            const state = await publicApi.confirm(secret);
            next.push({ secret, suffix: state.cardSuffix, state });
          }
          const item = next.at(-1)!;
          if (requests.current(token) && canDownload(item.state)) {
            try { await saveDelivery(token); }
            catch (cause) { item.downloadError = publicErrorMessage(cause); }
          }
        } catch (cause) { next.push({ secret, suffix: secret.slice(-8), error: publicErrorMessage(cause) }); }
        if (requests.current(token)) setResults([...next]);
      }
    });
  }

  async function checkedDelivery(secret: string, checked: RecoveryCheck, token: number) {
    if (!requests.current(token)) return undefined;
    if ((checked.credential.status === 'healthy' && checked.credential.hasOrder) || checked.recovery?.deliveryStatus === 'available') {
      // Restore this card's original order before reading or downloading it;
      // another card may own the browser's current access cookie.
      return publicApi.confirm(secret);
    }
    return undefined;
  }

  async function recoverAndPoll(secret: string, token: number): Promise<RecoveryCheck> {
    let credential = await publicApi.credentialStatus(secret);
    // The read-only check is the gate for recovery. A healthy or unrecoverable
    // credential must never enqueue a new reclaim task.
    for (let attempt = 0; attempt < 6 && requests.current(token) && credential.status === 'unknown' && credential.checkQueued; attempt += 1) {
      await new Promise((resolve) => window.setTimeout(resolve, 1000));
      if (!requests.current(token)) return { credential };
      credential = await publicApi.credentialStatus(secret);
    }
    if (!requests.current(token) || credential.status !== 'need_reclaim') return { credential };

    let recovery = await publicApi.recover(secret);
    for (let attempt = 0; attempt < 12 && requests.current(token) && (recovery.status === 'queued' || recovery.status === 'running' || recovery.status === 'retry_wait'); attempt += 1) {
      const delay = Math.max(1000, (recovery.retryAfterSeconds ?? 1) * 1000);
      await new Promise((resolve) => window.setTimeout(resolve, delay));
      if (!requests.current(token)) return { credential, recovery };
      recovery = await publicApi.recovery();
    }
    if (requests.current(token) && recovery.status === 'succeeded') {
      try { credential = await publicApi.credentialStatus(secret); } catch { /* Keep the completed recovery result visible. */ }
    }
    return { credential, recovery };
  }

  async function saveDelivery(token: number) {
    const file = await publicApi.download();
    if (!requests.current(token)) return;
    const url = URL.createObjectURL(file.blob);
    const anchor = document.createElement('a');
    anchor.href = url; anchor.download = file.filename;
    document.body.append(anchor); anchor.click(); anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  async function download(item: CardResult) {
    await operation(async (token) => {
      const state = item.secret ? await publicApi.confirm(item.secret) : await publicApi.state();
      if (!requests.current(token)) return;
      if (!canDownload(state)) throw new Error('public_delivery_pending');
      await saveDelivery(token);
      if (requests.current(token)) setResults((current) => current.map((row) => {
        if (row !== item) return row;
        const updated = { ...row, state };
        delete updated.downloadError;
        return updated;
      }));
    });
  }

  async function recover(item: CardResult) {
    if (!item.secret) return;
    await operation(async (token) => {
      const checked = await recoverAndPoll(item.secret, token);
      const state = await checkedDelivery(item.secret, checked, token);
      if (requests.current(token)) setResults((current) => current.map((row) => row.secret === item.secret
        ? { secret: row.secret, suffix: state?.cardSuffix ?? row.suffix, ...(checked.recovery ? { recovery: checked.recovery } : {}), credential: checked.credential, ...(state ? { state } : {}) }
        : row));
    });
  }

  return <Container size={1080} className="public-content public-redeem-page"><Stack gap={32}>
    <Group justify="space-between">
      <Title order={1} className="public-title">{mode === 'redeem' ? '卡密兑换' : '401 找回'}</Title>
      <SegmentedControl aria-label="客户操作" value={mode} onChange={(value) => setMode(value as 'redeem' | 'recover')}
        data={[{ value: 'redeem', label: '兑换' }, { value: 'recover', label: '401 找回' }]} disabled={busy} />
    </Group>
    <Paper withBorder radius={12} p={32} component="section" aria-label="输入卡密" className="public-input-panel"><Stack gap={20}>
      <Textarea label="卡密" placeholder="每行一张卡密" minRows={3} maxRows={8} autosize autoComplete="off"
        spellCheck={false} value={input} disabled={busy} onChange={(event) => setInput(event.currentTarget.value)} />
      <Group justify="space-between"><Text size="sm" c="dimmed">{cards.length} 张</Text>
        <Button loading={busy} disabled={busy || cards.length === 0 || cards.length > MAX_CARDS}
          onClick={() => void submit()}>{mode === 'redeem' ? '兑换卡密' : '检查并找回'}</Button>
      </Group>
      {cards.length > MAX_CARDS ? <Text c="error" role="alert">每次最多 {MAX_CARDS} 张</Text> : null}
    </Stack></Paper>
    <ActionNotice message={error} onClose={dismissError} />
    <Box className="public-results">{results.map((item, index) => <Paper key={`${item.secret}:${index}`} withBorder radius={12} p={24} className="public-result-row"
      component="section" aria-label={`卡密 ${item.suffix}`}><Stack gap={16}>
      <Group justify="space-between"><Text fw={600}>•••• {item.suffix}</Text>
        <Badge color={item.error ? 'error' : item.downloadError ? 'warning' : canDownload(item.state) || item.credential?.status === 'healthy' ? 'success' : 'warning'}>
          {item.error ? '操作未完成' : item.downloadError ? '下载未完成' : item.recovery ? recoveryLabel(item.recovery) : canDownload(item.state) ? '已兑换' : item.credential ? credentialLabel(item.credential) : '交付暂不可用'}
        </Badge>
      </Group>
      {item.state?.accountCount ? <Text size="sm" c="dimmed">{item.state.accountCount} 个账号</Text> : null}
      {item.credential && canDownload(item.state) ? <Text size="sm" c="dimmed">{credentialLabel(item.credential)}</Text> : null}
      {item.error ? <Text size="sm" c="error" role="alert">{item.error}</Text> : null}
      {item.downloadError ? <Text size="sm" c="error" role="alert">{item.downloadError}</Text> : null}
      <Group>
        {canDownload(item.state) ? <Button variant="default" disabled={busy} onClick={() => void download(item)}>下载 ZIP</Button> : null}
        {item.secret ? <Button variant="subtle" disabled={busy} onClick={() => void recover(item)}>
          {item.recovery || item.credential ? '重新检查' : '401 找回'}
        </Button> : null}
      </Group>
    </Stack></Paper>)}</Box>
  </Stack></Container>;
}
