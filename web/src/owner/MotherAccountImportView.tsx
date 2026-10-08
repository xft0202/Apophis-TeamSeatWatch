import ActionNotice from '../shared/ActionNotice';
import { useActionFeedback } from '../shared/useActionFeedback';
import { Box, Button, FileInput, Group, Paper, Stack, Table, Text, Textarea, Title } from '@mantine/core';
import { useMemo, useState } from 'react';
import type { components } from '../generated/owner';
import { importMotherAccounts, ownerProblem } from './auth';
import { validTwoFactorSecret } from './materialValidation';
import OwnerIcon from './OwnerIcon';
import StatusBadge from '../shared/StatusBadge';

type ImportResult = components['schemas']['MotherAccountImportResult'];
export default function MotherAccountImportView({ onBack, onImported }: { onBack: () => void; onImported: (result: ImportResult) => void }) {
  const [text, setText] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [notice, setNotice, dismissNotice] = useActionFeedback('');
  const [pending, setPending] = useState(false);
  const preview = useMemo(() => {
    const seen = new Set<string>();
    const lines = text.split(/\r?\n/);
    if (lines.at(-1) === '') lines.pop();
    return lines.map((line, index) => {
      const fields = line.split('----');
      const identifier = fields[0]?.trim().toLowerCase() ?? '';
      const valid = fields.length === 3 && identifier.length <= 254 && /^[^\s<>@]+@[^\s<>@]+$/.test(identifier)
        && Boolean(fields[1]) && fields[1]!.length <= 1024 && (fields[2]?.length ?? 0) <= 1024 && validTwoFactorSecret(fields[2] ?? '');
      const duplicate = valid && seen.has(identifier);
      if (valid) seen.add(identifier);
      return { line: index + 1, identifier: /^[^\s<>@]+@[^\s<>@]+$/.test(identifier) ? identifier : '—', valid, duplicate };
    });
  }, [text]);

  async function chooseFile(next: File | null) {
    setFile(next);
    if (!next) return;
    if (!next.name.toLowerCase().endsWith('.txt')) { setNotice('请选择 TXT 文件。'); return; }
    try { setText(await next.text()); setNotice(''); } catch { setNotice('文件读取失败，请重新选择。'); }
  }
  async function save() {
    if (pending || !text.trim() || preview.some((row) => !row.valid)) return;
    setPending(true); setNotice('');
    try { onImported(await importMotherAccounts(text)); }
    catch (error: unknown) { setNotice(ownerProblem(error).status === 403 ? '没有保存母号的权限。' : '母号导入失败，请重试。'); }
    finally { setPending(false); }
  }

  return <Stack gap={24} className="management-page mother-import-page">
    <Group className="management-subheader" justify="space-between"><Group gap={12}><Box className="management-page-icon"><OwnerIcon name="upload" size={20} /></Box><Title order={2}>导入母号</Title></Group><Button variant="default" disabled={pending} leftSection={<OwnerIcon name="arrow-left" size={16} />} onClick={onBack}>返回空间管理</Button></Group>
    <ActionNotice message={notice} onClose={dismissNotice} />
    <Paper withBorder radius={12} className="management-form-panel"><Stack gap={20}>
      <Group className="management-import-source" align="stretch"><Box className="management-file-zone"><OwnerIcon name="upload" size={24} /><Text fw={600}>TXT 文件</Text><FileInput value={file} onChange={chooseFile} disabled={pending} accept=".txt,text/plain" aria-label="选择 TXT 文件" placeholder="选择 TXT 文件" clearable /></Box><Textarea label="账号----密码----2FA" placeholder="mother@example.com----password----2FA" value={text} disabled={pending} onChange={(event) => setText(event.currentTarget.value)} minRows={8} autosize className="management-code-input" /></Group>
      {preview.length ? <Paper withBorder radius={8} className="management-preview-panel"><Group justify="space-between" mb="sm"><Text fw={600}>导入预览</Text><Text size="sm" c="dimmed">{preview.length} 行</Text></Group><Table className="management-table"><Table.Thead><Table.Tr><Table.Th>行号</Table.Th><Table.Th className="account-identity-cell">账号</Table.Th><Table.Th>状态</Table.Th></Table.Tr></Table.Thead><Table.Tbody>{preview.slice(0, 20).map((row) => <Table.Tr key={row.line}><Table.Td>{row.line}</Table.Td><Table.Td className="account-identity-cell">{row.identifier}</Table.Td><Table.Td><StatusBadge tone={!row.valid ? 'error' : row.duplicate ? 'warning' : 'gray'} label={!row.valid ? '格式待修正' : row.duplicate ? '重复' : '待导入'} /></Table.Td></Table.Tr>)}</Table.Tbody></Table></Paper> : null}
      <Group justify="flex-end" className="management-form-actions"><Button loading={pending} disabled={!text.trim() || preview.some((row) => !row.valid)} onClick={() => void save()}>确认导入</Button></Group>
    </Stack></Paper>
  </Stack>;
}
