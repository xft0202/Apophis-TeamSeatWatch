import { Box, Text, UnstyledButton } from '@mantine/core';

type Step = { label: string; status: string; complete: boolean; available: boolean };
export default function WorkflowSteps({ steps, current, onChange }: { steps: Step[]; current: number; onChange: (step: number) => void }) {
  return <Box className="workflow-steps" component="nav" aria-label="本轮操作步骤">{steps.map((step, index) => <UnstyledButton key={step.label} className="workflow-step" data-current={current === index || undefined} data-complete={step.complete || undefined} disabled={!step.available} aria-current={current === index ? 'step' : undefined} aria-label={`第 ${index + 1} 步 ${step.label} ${step.status}`} onClick={() => onChange(index)}><Text className="workflow-step-number" aria-hidden="true">{String(index + 1).padStart(2, '0')}</Text><Box><Text size="sm" fw={600}>{step.label}</Text><Text size="xs" c="dimmed">{step.status}</Text></Box></UnstyledButton>)}</Box>;
}
