import React, { useEffect, useRef, useState } from 'react';

interface CopyButtonProps {
  value: string;
  // What is copied, for the accessible name ("Copy builder URL").
  label: string;
}

// CopyButton copies a value to the clipboard and confirms it briefly.
export const CopyButton: React.FC<CopyButtonProps> = ({ value, label }) => {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const copy = () => {
    navigator.clipboard
      .writeText(value)
      .then(() => {
        setCopied(true);
        window.clearTimeout(timer.current);
        timer.current = window.setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => {
        // Clipboard access denied (insecure origin): the value stays selectable.
      });
  };

  return (
    <button
      type="button"
      className="ov-copy-btn"
      onClick={copy}
      aria-label={copied ? `${label} copied` : `Copy ${label}`}
      title={copied ? 'Copied' : `Copy ${label}`}
    >
      <i className={copied ? 'fas fa-check' : 'far fa-copy'} aria-hidden="true" />
    </button>
  );
};

interface CopyFieldProps {
  value: string;
  label: string;
}

// CopyField shows a full value (URL, key) in a monospace box with a copy button.
export const CopyField: React.FC<CopyFieldProps> = ({ value, label }) => (
  <div className="ov-copy-field">
    <code className="ov-copy-value" title={value}>
      {value}
    </code>
    <CopyButton value={value} label={label} />
  </div>
);

export function shortHash(hash: string, chars = 6): string {
  if (hash.length <= chars * 2 + 4) return hash;
  return `${hash.slice(0, chars + 2)}…${hash.slice(-chars)}`;
}
