import React from 'react';

interface SwitchProps {
  checked: boolean;
  onChange?: (next: boolean) => void;
  label: string;
  disabled?: boolean;
  busy?: boolean;
  // Explains a disabled switch (e.g. "Log in to change").
  title?: string;
}

// Switch is the one-click on/off control of a flow. It is a button with the
// switch role, so it is keyboard operable and announces its state.
export const Switch: React.FC<SwitchProps> = ({ checked, onChange, label, disabled, busy, title }) => (
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label}
    title={title ?? (checked ? `Disable ${label}` : `Enable ${label}`)}
    className={`bd-switch${busy ? ' bd-switch-busy' : ''}`}
    disabled={disabled || busy}
    onClick={(e) => {
      e.stopPropagation();
      onChange?.(!checked);
    }}
  />
);
