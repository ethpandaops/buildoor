// Package metrics holds buildoor's Prometheus collectors, served on the API
// server's /metrics endpoint.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Local build (testing_buildBlockV1) and transaction pool collectors.
var (
	TxPoolPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "buildoor_txpool_pending_txs",
		Help: "Transactions currently queued in the owned transaction pool.",
	})
	TxPoolGasSum = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "buildoor_txpool_pending_gas",
		Help: "Gas limit sum of the queued transactions.",
	})
	TxPoolAdmitted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "buildoor_txpool_admitted_total",
		Help: "Transactions admitted into the pool.",
	})
	TxPoolRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_txpool_rejected_total",
		Help: "Submissions the pool refused, by reason.",
	}, []string{"reason"})
	TxPoolEvicted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_txpool_evicted_total",
		Help: "Transactions that left the pool, by reason.",
	}, []string{"reason"})
	LocalBuildSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "buildoor_local_build_seconds",
		Help:    "Wall time of a local build (selection + testing_buildBlockV1 attempts).",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
	})
	LocalBuildTxs = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "buildoor_local_build_txs",
		Help:    "Transactions per locally built payload.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 14),
	})
	LocalBuildFillRatio = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "buildoor_local_build_fill_ratio",
		Help: "gasUsed / gasLimit of the last locally built payload.",
	})
	LocalBuildOutcomes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_local_build_outcomes_total",
		Help: "Local build outcomes, by status (ready, failed, skipped) and reason.",
	}, []string{"status", "reason"})
	LocalBuildSelected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_local_build_payload_source_total",
		Help: "Which payload fed the bids per build target: el or local.",
	}, []string{"source"})
	TxPlanChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_tx_plan_checks_total",
		Help: "Post-inclusion checks of locally built blocks against their plan, by verdict.",
	}, []string{"status"})
)
