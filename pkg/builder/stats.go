package builder

// BuilderStats tracks statistics for builder operations.
type BuilderStats struct {
	SlotsBuilt     uint64
	BidsSubmitted  uint64
	BidsWon        uint64
	BlocksIncluded uint64 // Blocks where our payload was included
	TotalPaid      uint64 // Gwei paid for won bids
	RevealsSuccess uint64
	RevealsFailed  uint64
	RevealsSkipped uint64
}

// incrementStat safely increments statistics.
func (s *Service) incrementStat(fn func(*BuilderStats)) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()

	fn(s.stats)
}

// IncrementBidsSubmitted increments the bids submitted counter.
// Called by the ePBS service when a bid is submitted.
func (s *Service) IncrementBidsSubmitted() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.BidsSubmitted++
	})
}

// IncrementBlocksIncluded increments the blocks included and bids won counters.
// Called by the ePBS service when our payload is included in a beacon block.
func (s *Service) IncrementBlocksIncluded() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.BlocksIncluded++
		stats.BidsWon++
	})
}

// IncrementRevealsSuccess increments the successful reveals counter.
func (s *Service) IncrementRevealsSuccess() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.RevealsSuccess++
	})
}

// IncrementRevealsFailed increments the failed reveals counter.
func (s *Service) IncrementRevealsFailed() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.RevealsFailed++
	})
}

// IncrementRevealsSkipped increments the skipped reveals counter.
func (s *Service) IncrementRevealsSkipped() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.RevealsSkipped++
	})
}
