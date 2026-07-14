import React, { useState } from 'react';

// Backend cap on the number of slots one update may target
// (action_plan.MaxSlotsPerUpdate).
const MAX_SLOTS_PER_UPDATE = 6400;

interface BulkEditBarProps {
  selectionCount: number;
  canEdit: boolean;
  currentSlot: number;
  onClearSelection: () => void;
  onEditSelected: () => void;
  onEditRange: (fromSlot: number, toSlot: number) => void;
}

export const BulkEditBar: React.FC<BulkEditBarProps> = ({
  selectionCount,
  canEdit,
  currentSlot,
  onClearSelection,
  onEditSelected,
  onEditRange,
}) => {
  const [fromInput, setFromInput] = useState('');
  const [toInput, setToInput] = useState('');
  const [rangeError, setRangeError] = useState<string | null>(null);

  const handleEditRange = (e: React.FormEvent) => {
    e.preventDefault();
    setRangeError(null);

    const from = parseInt(fromInput, 10);
    const to = parseInt(toInput, 10);

    if (isNaN(from) || isNaN(to) || from < 0 || to < 0) {
      setRangeError('From/to slots must be non-negative numbers');
      return;
    }
    if (to < from) {
      setRangeError('To slot must be >= from slot');
      return;
    }
    if (to - from + 1 > MAX_SLOTS_PER_UPDATE) {
      setRangeError(`Range too large: max ${MAX_SLOTS_PER_UPDATE} slots per update`);
      return;
    }
    if (to <= currentSlot) {
      setRangeError('Range is entirely in the past — only future slots can be edited');
      return;
    }

    onEditRange(from, to);
  };

  return (
    <div className="d-flex flex-wrap align-items-center gap-2 mb-2">
      {selectionCount > 0 && (
        <>
          <span className="badge bg-primary">{selectionCount} slots selected</span>
          <button
            type="button"
            className="btn btn-sm btn-primary"
            onClick={onEditSelected}
            disabled={!canEdit}
            title={canEdit ? 'Edit the selected slots' : 'Login required to edit plans'}
          >
            <i className="fas fa-pencil-alt me-1"></i>
            Edit selected
          </button>
          <button type="button" className="btn btn-sm btn-outline-secondary" onClick={onClearSelection}>
            Clear
          </button>
          <span className="vr d-none d-md-block"></span>
        </>
      )}

      <form className="d-flex flex-wrap align-items-center gap-2" onSubmit={handleEditRange}>
        <span className="text-muted small">Range:</span>
        <input
          type="number"
          className="form-control form-control-sm ap-range-input"
          placeholder="From slot"
          min={0}
          value={fromInput}
          onChange={(e) => setFromInput(e.target.value)}
        />
        <input
          type="number"
          className="form-control form-control-sm ap-range-input"
          placeholder="To slot"
          min={0}
          value={toInput}
          onChange={(e) => setToInput(e.target.value)}
        />
        <button
          type="submit"
          className="btn btn-sm btn-outline-primary"
          disabled={!canEdit || fromInput === '' || toInput === ''}
          title={canEdit ? 'Edit an explicit slot range (may extend beyond the grid)' : 'Login required to edit plans'}
        >
          Edit range&hellip;
        </button>
        {rangeError && <span className="text-danger small">{rangeError}</span>}
      </form>
    </div>
  );
};
