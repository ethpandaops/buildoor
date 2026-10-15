import React from 'react';

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral' | 'accent';

interface TagProps {
  tone?: Tone;
  title?: string;
  className?: string;
  children: React.ReactNode;
}

export const Tag: React.FC<TagProps> = ({ tone = 'neutral', title, className, children }) => (
  <span className={`bd-tag bd-tone-${tone}${className ? ` ${className}` : ''}`} title={title}>
    {children}
  </span>
);
