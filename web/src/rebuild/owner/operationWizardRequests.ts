function requestGeneration() {
  let current = 0;
  return {
    begin: () => ++current,
    isCurrent: (value: number) => value === current,
    invalidate: () => { current++; },
  };
}

export function createOperationWizardRequests() {
  const preview = requestGeneration();
  const childDetails = requestGeneration();
  const workspace = requestGeneration();
  return {
    preview,
    childDetails,
    workspace,
    invalidateAll: () => {
      preview.invalidate();
      childDetails.invalidate();
      workspace.invalidate();
    },
  };
}
