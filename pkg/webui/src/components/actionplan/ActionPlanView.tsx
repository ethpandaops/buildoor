import React, { useCallback, useRef, useState } from 'react';
import { useEventStream } from '../../hooks/useEventStream';
import { useActionPlan } from '../../hooks/useActionPlan';
import { useAuthContext } from '../../context/AuthContext';
import { ActionPlanGrid } from './ActionPlanGrid';
import { BulkEditBar } from './BulkEditBar';
import { SlotEditModal, type ModalTarget } from './SlotEditModal';

// Default epoch window around the current epoch.
const EPOCHS_BACK = 8;
const EPOCHS_AHEAD = 4;

interface EpochWindow {
  start: number;
  end: number;
}

export const ActionPlanView: React.FC = () => {
  const { chainInfo, currentSlot } = useEventStream();
  const { isLoggedIn } = useAuthContext();
  const canEdit = isLoggedIn;

  const slotsPerEpoch = chainInfo?.slots_per_epoch ?? 0;
  const currentEpoch = slotsPerEpoch > 0 ? Math.floor(currentSlot / slotsPerEpoch) : 0;

  // The view follows the current epoch by default; navigating (Older/Newer/
  // jump) pins an explicit window until "Now" resumes following.
  const [follow, setFollow] = useState(true);
  const [pinned, setPinned] = useState<EpochWindow | null>(null);
  const [jumpInput, setJumpInput] = useState('');

  const ready = !!chainInfo && slotsPerEpoch > 0 && currentSlot > 0;

  // The effective window: derived live from the current epoch while following
  // (so it advances as epochs pass), otherwise the pinned range.
  const window: EpochWindow | null = !ready
    ? null
    : follow
      ? { start: Math.max(0, currentEpoch - EPOCHS_BACK), end: currentEpoch + EPOCHS_AHEAD }
      : pinned;

  const minSlot = window ? window.start * slotsPerEpoch : 0;
  const maxSlot = window ? (window.end + 1) * slotsPerEpoch - 1 : -1;

  const { plans, results, loading, error, refetch, applyUpdates } = useActionPlan(minSlot, maxSlot);

  // Selection (future slots only) + range anchor for shift-click.
  const [selection, setSelection] = useState<Set<number>>(new Set());
  const lastClickedRef = useRef<number | null>(null);

  const [modalTarget, setModalTarget] = useState<ModalTarget | null>(null);

  const handleCellClick = useCallback(
    (slot: number, shiftKey: boolean) => {
      if (shiftKey && lastClickedRef.current !== null) {
        // Extend a range selection from the last clicked slot; only future
        // slots are selectable (past/frozen slots reject edits anyway).
        const from = Math.min(lastClickedRef.current, slot);
        const to = Math.max(lastClickedRef.current, slot);
        setSelection((prev) => {
          const next = new Set(prev);
          for (let s = from; s <= to; s++) {
            if (s > currentSlot) next.add(s);
          }
          return next;
        });
        lastClickedRef.current = slot;
        return;
      }

      lastClickedRef.current = slot;
      setModalTarget({ slots: [slot] });
    },
    [currentSlot]
  );

  const handleEditSelected = useCallback(() => {
    if (selection.size === 0) return;
    setModalTarget({ slots: [...selection].sort((a, b) => a - b) });
  }, [selection]);

  const handleEditRange = useCallback((fromSlot: number, toSlot: number) => {
    setModalTarget({ fromSlot, toSlot });
  }, []);

  // Shifting pins the window (stops following) so the manual position sticks.
  const shiftWindow = (epochs: number) => {
    if (!window) return;
    const span = window.end - window.start;
    const start = Math.max(0, window.start + epochs);
    setPinned({ start, end: start + span });
    setFollow(false);
  };

  const jumpToCurrent = () => {
    setFollow(true);
  };

  const handleJump = (e: React.FormEvent) => {
    e.preventDefault();
    const epoch = parseInt(jumpInput, 10);
    if (isNaN(epoch) || epoch < 0) return;
    setPinned({ start: Math.max(0, epoch - EPOCHS_BACK), end: epoch + EPOCHS_AHEAD });
    setFollow(false);
  };

  if (!chainInfo || window === null) {
    return (
      <div className="container-fluid mt-2 text-center py-5 text-muted">
        <div className="spinner-border spinner-border-sm me-2" role="status"></div>
        Waiting for chain info...
      </div>
    );
  }

  return (
    <div className="container-fluid mt-2">
      <div className="card">
        <div className="card-header d-flex flex-wrap align-items-center gap-2">
          <h5 className="mb-0">Action Plan</h5>
          <span className="text-muted small">
            epochs {window.start}–{window.end} (current: {currentEpoch})
            {follow && <span className="badge bg-success ms-2">live</span>}
          </span>
          {loading && <span className="spinner-border spinner-border-sm text-primary"></span>}

          <div className="d-flex flex-wrap align-items-center gap-2 ms-auto">
            <div className="btn-group btn-group-sm">
              <button type="button" className="btn btn-outline-secondary" onClick={() => shiftWindow(-(window.end - window.start + 1))} disabled={window.start === 0} title="Older epochs">
                <i className="fas fa-chevron-left"></i> Older
              </button>
              <button type="button" className={`btn ${follow ? 'btn-primary' : 'btn-outline-secondary'}`} onClick={jumpToCurrent} title="Follow the current epoch">
                Now
              </button>
              <button type="button" className="btn btn-outline-secondary" onClick={() => shiftWindow(window.end - window.start + 1)} title="Newer epochs">
                Newer <i className="fas fa-chevron-right"></i>
              </button>
            </div>
            <form className="d-flex align-items-center gap-1" onSubmit={handleJump}>
              <input
                type="number"
                className="form-control form-control-sm ap-range-input"
                placeholder="Epoch"
                min={0}
                value={jumpInput}
                onChange={(e) => setJumpInput(e.target.value)}
              />
              <button type="submit" className="btn btn-sm btn-outline-secondary" disabled={jumpInput === ''}>
                Go
              </button>
            </form>
            <button type="button" className="btn btn-sm btn-outline-secondary" onClick={refetch} title="Refresh">
              <i className="fas fa-rotate"></i>
            </button>
          </div>
        </div>

        <div className="card-body">
          {error && <div className="alert alert-danger small py-2">{error}</div>}

          <BulkEditBar
            selectionCount={selection.size}
            canEdit={canEdit}
            currentSlot={currentSlot}
            onClearSelection={() => setSelection(new Set())}
            onEditSelected={handleEditSelected}
            onEditRange={handleEditRange}
          />

          <ActionPlanGrid
            startEpoch={window.start}
            endEpoch={window.end}
            slotsPerEpoch={slotsPerEpoch}
            currentSlot={currentSlot}
            plans={plans}
            results={results}
            selection={selection}
            onCellClick={handleCellClick}
          />

          <div className="d-flex flex-wrap align-items-center gap-3 mt-2 timeline-legend">
            <span className="legend-section">Plan:</span>
            <span><span className="ap-chip ap-chip-custom">B</span> bid</span>
            <span><span className="ap-chip ap-chip-custom">A</span> builder api</span>
            <span><span className="ap-chip ap-chip-custom">R</span> reveal</span>
            <span><span className="ap-chip ap-chip-reorg">P</span> reorg parent</span>
            <span><span className="ap-chip ap-chip-transform">jq</span> transform</span>
            <span><span className="ap-chip ap-chip-disabled">B</span> disabled</span>
            <span className="legend-section">Bid (left dot):</span>
            <span><span className="ap-dot ap-dot-included d-inline-block"></span> included</span>
            <span><span className="ap-dot ap-dot-bidding d-inline-block"></span> bid, not included</span>
            <span><span className="ap-dot ap-dot-failed d-inline-block"></span> failed</span>
            <span><span className="ap-dot ap-dot-idle d-inline-block"></span> nothing happened</span>
            <span><span className="ap-dot ap-dot-built d-inline-block"></span> built only</span>
            <span className="legend-section">Payload (right dot, won slots):</span>
            <span><span className="ap-dot ap-dot-payload-canonical d-inline-block"></span> canonical</span>
            <span><span className="ap-dot ap-dot-payload-missed d-inline-block"></span> missed</span>
            <span><span className="ap-dot ap-dot-payload-orphaned d-inline-block"></span> reorged out</span>
            <span><span className="ap-dot ap-dot-payload-pending d-inline-block"></span> pending</span>
            <span className="text-muted">Click a slot for details; shift-click selects a range.</span>
          </div>
        </div>
      </div>

      {modalTarget && (
        <SlotEditModal
          key={`${modalTarget.slots?.join(',') ?? ''}:${modalTarget.fromSlot ?? ''}:${modalTarget.toSlot ?? ''}`}
          target={modalTarget}
          plans={plans}
          results={results}
          currentSlot={currentSlot}
          canEdit={canEdit}
          applyUpdates={applyUpdates}
          onClose={() => setModalTarget(null)}
        />
      )}
    </div>
  );
};
