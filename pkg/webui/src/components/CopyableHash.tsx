import React, { useState } from 'react';

function truncate(hash: string, chars: number, headOnly: boolean): string {
  if (headOnly) return hash.length <= chars + 2 ? hash : `${hash.substring(0, chars + 2)}…`;
  if (hash.length <= chars * 2 + 2) return hash;
  return `${hash.substring(0, chars + 2)}...${hash.substring(hash.length - chars)}`;
}

interface CopyableHashProps {
  value: string;
  chars?: number;
  className?: string;
  // When set, the full value is shown untruncated (still copyable on click).
  full?: boolean;
  // When set, only the head of the value is shown ("0x5e43a1…").
  headOnly?: boolean;
}

export const CopyableHash: React.FC<CopyableHashProps> = ({ value, chars = 8, className = '', full = false, headOnly = false }) => {
  const [copied, setCopied] = useState(false);

  const handleClick = () => {
    navigator.clipboard
      .writeText(value)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch((err) => {
        console.error('Failed to copy:', err);
      });
  };

  return (
    <span
      title={`${value}\nClick to copy`}
      onClick={handleClick}
      style={{ cursor: 'pointer' }}
      className={`${copied ? 'text-success' : ''} ${className}`.trim()}
    >
      {copied ? 'Copied!' : full ? value : truncate(value, chars, headOnly)}
    </span>
  );
};
