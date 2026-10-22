package payload_bidder

import (
	"context"
	"fmt"
	"sync"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// targetPublishers lazily opens one beacon client per reveal-target URL and
// keeps it for the process lifetime; the set of URLs a devnet plan names is
// small and stable.
type targetPublishers struct {
	mu      sync.Mutex
	clients map[string]envelopePublisher
	log     logrus.FieldLogger
}

func newTargetPublishers(log logrus.FieldLogger) *targetPublishers {
	return &targetPublishers{
		clients: make(map[string]envelopePublisher, 4),
		log:     log,
	}
}

// get returns the publisher for a beacon REST base URL, opening it on first use.
func (p *targetPublishers) get(ctx context.Context, url string) (envelopePublisher, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client, ok := p.clients[url]; ok {
		return client, nil
	}

	client, err := beacon.NewClient(ctx, url, p.log)
	if err != nil {
		return nil, fmt.Errorf("failed to open beacon client for reveal target %s: %w", url, err)
	}

	p.clients[url] = client

	return client, nil
}

// publishTargets starts the slot's additional envelope publications after the
// primary reveal succeeded. Each target runs on its own goroutine: it waits
// its delay, rebuilds and re-signs the envelope under its own transform and
// submits it to every listed beacon node (the primary node when none is
// listed). Failures are reported as reveal results and never touch the
// primary reveal's outcome.
func (s *RevealService) publishTargets(slot phase0.Slot, state *revealState) {
	for i, target := range state.settings.Targets {
		s.wg.Add(1)

		go func(index int, target action_plan.RevealTarget) {
			defer s.wg.Done()

			s.publishTarget(slot, state, index, target)
		}(i, target)
	}
}

func (s *RevealService) publishTarget(slot phase0.Slot, state *revealState, index int,
	target action_plan.RevealTarget) {
	log := s.log.WithFields(logrus.Fields{
		"slot":   slot,
		"target": index,
	})

	if target.DelayMs > 0 {
		select {
		case <-time.After(time.Duration(target.DelayMs) * time.Millisecond):
		case <-s.ctx.Done():
			return
		}
	}

	urls := target.URLs
	if len(urls) == 0 {
		urls = []string{""}
	}

	validation := state.settings.BroadcastValidation
	if target.BroadcastValidation != nil {
		validation = *target.BroadcastValidation
	}

	startedAt := time.Now()

	envelope, blobs, proofs, err := s.buildEnvelope(state.req, target.Transform)
	if err != nil {
		log.WithError(err).Error("Failed to build reveal target envelope")

		for _, url := range urls {
			s.fireTargetResult(slot, state, url, startedAt, nil, err)
		}

		return
	}

	for _, url := range urls {
		publisher := s.publisher

		if url != "" {
			publisher, err = s.targets.get(s.ctx, url)
			if err != nil {
				log.WithError(err).Error("Failed to open reveal target")
				s.fireTargetResult(slot, state, url, startedAt, envelope, err)

				continue
			}
		}

		err = s.publishTo(publisher, envelope, blobs, proofs, validation)
		if err != nil {
			log.WithError(err).WithField("url", url).Error("Failed to submit reveal target envelope")
		} else {
			log.WithFields(logrus.Fields{
				"url":         url,
				"transformed": target.Transform != "",
			}).Info("Reveal target envelope published")
		}

		s.fireTargetResult(slot, state, url, startedAt, envelope, err)
	}
}

func (s *RevealService) fireTargetResult(slot phase0.Slot, state *revealState, url string,
	startedAt time.Time, envelope *eth2all.SignedExecutionPayloadEnvelope, err error) {
	result := &RevealResult{
		Slot:        slot,
		Transport:   state.req.Transport,
		Target:      url,
		Success:     err == nil,
		Attempt:     1,
		MaxAttempts: 1,
		StartedAt:   startedAt,
		CompletedAt: time.Now(),
		Envelope:    envelope,
	}

	if url == "" {
		result.Target = RevealTargetPrimary
	}

	if err != nil {
		result.Error = err.Error()
	}

	s.results.Fire(result)
}
