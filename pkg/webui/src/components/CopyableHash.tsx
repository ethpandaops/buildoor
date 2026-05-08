import React, { useState } from 'react';

function truncate(hash: string, chars: number): string {
  if (hash.length <= chars * 2 + 2) return hash;
  return `${hash.substring(0, chars + 2)}...${hash.substring(hash.length - chars)}`;
}

interface CopyableHashProps {
  value: string;
  chars?: number;
  className?: string;
}

export const CopyableHash: React.FC<CopyableHashProps> = ({ value, chars = 8, className = '' }) => {
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
      {copied ? 'Copied!' : truncate(value, chars)}
    </span>
  );
};
