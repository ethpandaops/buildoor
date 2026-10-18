import React from 'react';

interface StatTileProps {
  label: string;
  value: React.ReactNode;
  note?: React.ReactNode;
  title?: string;
}

export const StatTile: React.FC<StatTileProps> = ({ label, value, note, title }) => (
  <div className="bd-stat" title={title}>
    <div className="bd-stat-label">{label}</div>
    <div className="bd-stat-value">{value}</div>
    {note !== undefined && <div className="bd-stat-note">{note}</div>}
  </div>
);
