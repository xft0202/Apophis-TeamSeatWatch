import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react';

export const ACTION_FEEDBACK_MS = 3000;

export function useActionFeedback<Value>(emptyValue: Value, initialValue = emptyValue) {
  const empty = useRef(emptyValue).current;
  const [feedback, setFeedback] = useState({ value: initialValue, version: 0 });
  const setValue: Dispatch<SetStateAction<Value>> = useCallback(value => {
    setFeedback(current => ({
      value: typeof value === 'function' ? (value as (previous: Value) => Value)(current.value) : value,
      version: current.version + 1,
    }));
  }, []);
  const dismiss = useCallback(() => setValue(empty), [empty, setValue]);

  useEffect(() => {
    if (Object.is(feedback.value, empty)) return;
    const version = feedback.version;
    const timer = window.setTimeout(() => {
      setFeedback(current => current.version === version ? { ...current, value: empty } : current);
    }, ACTION_FEEDBACK_MS);
    return () => window.clearTimeout(timer);
  }, [empty, feedback.version, feedback.value]);

  return [feedback.value, setValue, dismiss] as const;
}
