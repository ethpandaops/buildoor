// Package metrics holds buildoor's Prometheus collectors, served on the API
// server's /metrics endpoint.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Testing build source collectors.
var (
	TestingQueueTxs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "buildoor_testing_queue_txs",
		Help: "Transactions currently held in the tx intake queue.",
	})
	TestingQueueEvictions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_testing_queue_evictions_total",
		Help: "Transactions evicted from the tx intake queue, by reason.",
	}, []string{"reason"})
	TestingBuildSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "buildoor_testing_build_seconds",
		Help:    "Wall time of a testing build (pack + testing_buildBlockV1 attempts).",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 10),
	})
	TestingBuildTxs = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "buildoor_testing_build_txs",
		Help:    "Transactions per testing-built payload.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 14),
	})
	TestingBuildFillRatio = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "buildoor_testing_build_fill_ratio",
		Help: "gasUsed / gasLimit of the last testing-built payload.",
	})
	TestingBuildFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_testing_build_failures_total",
		Help: "Testing builds that produced no payload, by reason.",
	}, []string{"reason"})
	TestingPlanChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "buildoor_testing_plan_checks_total",
		Help: "Tx plan verifications against included blocks, by verdict.",
	}, []string{"status"})
)
