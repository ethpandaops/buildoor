import React, { useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';

interface InfoHintProps {
  children: React.ReactNode;
  // Opens the callout to the left of the icon (for hints near a right edge).
  alignEnd?: boolean;
}

const CALLOUT_GAP = 6;
const VIEWPORT_MARGIN = 8;

// InfoHint is a small "i" icon that explains a setting on hover or keyboard
// focus, keeping the panels compact while every option stays explained. The
// callout renders into document.body at a fixed position, so scrolling tables,
// modal bodies and cards with overflow never clip it.
export const InfoHint: React.FC<InfoHintProps> = ({ children, alignEnd }) => {
  const iconRef = useRef<HTMLSpanElement>(null);
  const calloutRef = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<{ top: number; left: number } | null>(null);

  useLayoutEffect(() => {
    if (!open || !iconRef.current || !calloutRef.current) {
      return;
    }

    const icon = iconRef.current.getBoundingClientRect();
    const callout = calloutRef.current.getBoundingClientRect();

    let left = alignEnd ? icon.right - callout.width : icon.left - VIEWPORT_MARGIN;
    left = Math.min(left, window.innerWidth - callout.width - VIEWPORT_MARGIN);
    left = Math.max(left, VIEWPORT_MARGIN);

    // Below the icon, or above it when the callout would leave the viewport.
    let top = icon.bottom + CALLOUT_GAP;
    if (top + callout.height > window.innerHeight - VIEWPORT_MARGIN && icon.top - CALLOUT_GAP - callout.height > 0) {
      top = icon.top - CALLOUT_GAP - callout.height;
    }

    setPosition({ top, left });
  }, [open, alignEnd]);

  const show = () => setOpen(true);
  const hide = () => {
    setOpen(false);
    setPosition(null);
  };

  return (
    <span
      ref={iconRef}
      className="info-hint"
      tabIndex={0}
      role="note"
      onMouseEnter={show}
      onMouseLeave={hide}
      onFocus={show}
      onBlur={hide}
      onClick={(e) => e.stopPropagation()}
    >
      <i className="fas fa-circle-info" aria-hidden="true"></i>
      {open && createPortal(
        <span
          ref={calloutRef}
          className="info-hint-callout"
          style={position
            ? { top: position.top, left: position.left }
            : { top: 0, left: 0, visibility: 'hidden' }}
        >
          {children}
        </span>,
        document.body
      )}
    </span>
  );
};
