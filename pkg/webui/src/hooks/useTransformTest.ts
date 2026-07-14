import { useState, useRef, useCallback, useEffect } from 'react';

export type TransformTarget = 'payload' | 'bid' | 'envelope';

export interface TransformTestResult {
  target: TransformTarget;
  input: string; // pretty-printed sample JSON
  input_source: string; // "template" | "artifact:slot-N"
  output?: string; // pretty-printed transformed JSON (when no error)
  error?: string; // parse/eval error
}

interface TransformTestState {
  result: TransformTestResult | null;
  loading: boolean;
  requestError: string | null;
}

/**
 * useTransformTest evaluates a jq expression against a sample builder object
 * server-side (the exact gojq used in production), debounced, so the action
 * plan modal can live-preview transforms as the operator types. Passing an
 * empty expression previews the identity (the sample input itself).
 */
export function useTransformTest(
  target: TransformTarget,
  expression: string,
  sampleSlot: number | undefined,
  enabled: boolean
): TransformTestState {
  const [state, setState] = useState<TransformTestState>({
    result: null,
    loading: false,
    requestError: null,
  });

  // Sequence guard so a slow earlier request can't overwrite a newer result.
  const seqRef = useRef(0);

  const run = useCallback(async () => {
    const seq = ++seqRef.current;
    setState((prev) => ({ ...prev, loading: true, requestError: null }));

    try {
      const res = await fetch('/api/buildoor/action-plan/test-transform', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          target,
          expression,
          ...(sampleSlot && sampleSlot > 0 ? { sample_slot: sampleSlot } : {}),
        }),
      });

      if (!res.ok) {
        const text = await res.text();
        if (seq === seqRef.current) {
          setState({ result: null, loading: false, requestError: text || res.statusText });
        }
        return;
      }

      const result: TransformTestResult = await res.json();
      if (seq === seqRef.current) {
        setState({ result, loading: false, requestError: null });
      }
    } catch (err) {
      if (seq === seqRef.current) {
        setState({
          result: null,
          loading: false,
          requestError: err instanceof Error ? err.message : 'request failed',
        });
      }
    }
  }, [target, expression, sampleSlot]);

  useEffect(() => {
    if (!enabled) {
      setState({ result: null, loading: false, requestError: null });
      return;
    }

    const handle = setTimeout(run, 300); // debounce keystrokes
    return () => clearTimeout(handle);
  }, [enabled, run]);

  return state;
}
