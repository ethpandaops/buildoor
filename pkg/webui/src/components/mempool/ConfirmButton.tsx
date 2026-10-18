import React, { useEffect, useState } from 'react';

interface ConfirmButtonProps {
  // Content of the idle button.
  children: React.ReactNode;
  // Question shown next to the confirm/cancel pair.
  prompt: React.ReactNode;
  confirmLabel?: string;
  onConfirm: () => void | Promise<void>;
  disabled?: boolean;
  title?: string;
  className?: string;
}

const REARM_MS = 6000;

// ConfirmButton is a two-step destructive action: the first click turns the
// button into an inline "prompt + Confirm / Cancel" pair, the second click
// runs the action. An unanswered prompt falls back to the idle button.
export const ConfirmButton: React.FC<ConfirmButtonProps> = ({
  children,
  prompt,
  confirmLabel = 'Confirm',
  onConfirm,
  disabled,
  title,
  className,
}) => {
  const [armed, setArmed] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!armed) return undefined;
    const timer = setTimeout(() => setArmed(false), REARM_MS);
    return () => clearTimeout(timer);
  }, [armed]);

  if (!armed) {
    return (
      <button
        type="button"
        className={className ?? 'bd-btn bd-btn-sm bd-btn-danger'}
        disabled={disabled}
        title={title}
        onClick={() => setArmed(true)}
      >
        {children}
      </button>
    );
  }

  return (
    <span className="bd-mp-confirm" role="group">
      <span className="bd-mp-confirm-prompt">{prompt}</span>
      <button
        type="button"
        className="bd-btn bd-btn-sm bd-btn-danger"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          try {
            await onConfirm();
          } finally {
            setBusy(false);
            setArmed(false);
          }
        }}
      >
        {confirmLabel}
      </button>
      <button type="button" className="bd-btn bd-btn-sm" disabled={busy} onClick={() => setArmed(false)}>
        Cancel
      </button>
    </span>
  );
};
