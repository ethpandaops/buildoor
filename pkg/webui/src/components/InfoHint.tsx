import React from 'react';

interface InfoHintProps {
  children: React.ReactNode;
  // Opens the callout to the left of the icon (for hints near a right edge).
  alignEnd?: boolean;
}

// InfoHint is a small "i" icon that explains a setting on hover or keyboard
// focus, keeping the panels compact while every option stays explained.
export const InfoHint: React.FC<InfoHintProps> = ({ children, alignEnd }) => (
  <span className={`info-hint${alignEnd ? ' info-hint-end' : ''}`} tabIndex={0} role="note">
    <i className="fas fa-circle-info" aria-hidden="true"></i>
    <span className="info-hint-callout">{children}</span>
  </span>
);
