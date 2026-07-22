import React, { useState } from 'react';
import { useTransformTest, type TransformTarget } from '../../hooks/useTransformTest';

interface TransformFieldProps {
  target: TransformTarget;
  label: string;
  hint: string;
  value: string;
  disabled: boolean;
  sampleSlot: number | undefined;
  onChange: (next: string) => void;
}

// One expression input + collapsible live preview for a single transform target.
const TransformField: React.FC<TransformFieldProps> = ({
  target,
  label,
  hint,
  value,
  disabled,
  sampleSlot,
  onChange,
}) => {
  const [showPreview, setShowPreview] = useState(false);
  const { result, loading, requestError } = useTransformTest(target, value, sampleSlot, showPreview);

  return (
    <div className="ap-transform-field">
      <div className="d-flex align-items-center gap-2 mb-1">
        <label className="form-label small mb-0 fw-semibold">{label}</label>
        <button
          type="button"
          className="btn btn-link btn-sm p-0 ms-auto"
          onClick={() => setShowPreview((v) => !v)}
        >
          {showPreview ? 'Hide test' : 'Test'}
        </button>
      </div>
      <textarea
        className="form-control form-control-sm font-monospace ap-transform-input"
        rows={2}
        placeholder="jq expression, e.g. .gas_limit = &quot;60000000&quot;  (empty = no transform)"
        value={value}
        disabled={disabled}
        spellCheck={false}
        onChange={(e) => onChange(e.target.value)}
      />
      <div className="form-text mt-0">{hint}</div>

      {showPreview && (
        <div className="ap-transform-preview mt-1">
          {loading && <span className="spinner-border spinner-border-sm text-primary"></span>}
          {requestError && <div className="alert alert-danger small py-1 mb-1">{requestError}</div>}
          {result?.error && (
            <div className="alert alert-warning small py-1 mb-1">
              <i className="fas fa-triangle-exclamation me-1"></i>
              {result.error}
            </div>
          )}
          {result && (
            <div className="row g-2">
              <div className="col-12 col-lg-6">
                <div className="ap-transform-io-label">
                  Input <span className="text-muted">({result.input_source})</span>
                </div>
                <pre className="ap-transform-io">{result.input}</pre>
              </div>
              <div className="col-12 col-lg-6">
                <div className="ap-transform-io-label">Output</div>
                <pre className={`ap-transform-io ${result.error ? 'ap-transform-io-stale' : ''}`}>
                  {result.output ?? '—'}
                </pre>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export interface TransformState {
  payload: string;
  bid: string;
  envelope: string;
}

interface TransformEditorProps {
  bulk: boolean;
  value: TransformState;
  disabled: boolean;
  // Slot whose captured artifacts seed the live-test samples (past slots);
  // undefined falls back to templates server-side.
  sampleSlot: number | undefined;
  onChange: (next: TransformState) => void;
}

// TransformEditor exposes the three jq transform slots with live server-side
// testing. Expressions run against the object's JSON just before it is used /
// signed in production (bid & envelope on the message, then re-signed).
export const TransformEditor: React.FC<TransformEditorProps> = ({
  bulk,
  value,
  disabled,
  sampleSlot,
  onChange,
}) => (
  <div className="mb-3">
    <div className="section-header mb-1">Transforms (jq)</div>
    <div className="form-text mt-0 mb-2">
      Advanced: rewrite the object&apos;s JSON with a jq expression for custom builder testing.
      Bid and envelope transforms run on the message and are re-signed. Leave empty for no change.
      {bulk && ' Applied to every targeted slot; empty leaves each slot as it is.'}
    </div>
    <TransformField
      target="payload"
      label="Payload"
      hint="Rewrites the built execution payload before it feeds the bid commitment and the reveal."
      value={value.payload}
      disabled={disabled}
      sampleSlot={sampleSlot}
      onChange={(v) => onChange({ ...value, payload: v })}
    />
    <TransformField
      target="bid"
      label="Bid message"
      hint="Rewrites the bid message just before signing; re-signed afterwards."
      value={value.bid}
      disabled={disabled}
      sampleSlot={sampleSlot}
      onChange={(v) => onChange({ ...value, bid: v })}
    />
    <TransformField
      target="envelope"
      label="Envelope message"
      hint="Rewrites the envelope message (incl. .payload) just before signing; re-signed afterwards."
      value={value.envelope}
      disabled={disabled}
      sampleSlot={sampleSlot}
      onChange={(v) => onChange({ ...value, envelope: v })}
    />
  </div>
);
