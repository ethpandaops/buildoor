import React, { useState } from 'react';

// CopyButton copies a value to the clipboard and confirms with a check mark.
export const CopyButton: React.FC<{ value: string; title?: string }> = ({ value, title }) => {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="bd-btn bd-btn-sm bd-mp-icon-btn"
      title={title ?? 'Copy to clipboard'}
      aria-label={title ?? 'Copy to clipboard'}
      onClick={() => {
        navigator.clipboard.writeText(value).then(() => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }).catch(() => undefined);
      }}
    >
      <i className={`fas ${copied ? 'fa-check' : 'fa-copy'}`}></i>
    </button>
  );
};
