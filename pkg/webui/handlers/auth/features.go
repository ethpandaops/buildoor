package auth

import "github.com/ethpandaops/service-authenticatoor/pkg/auth"

// The features of buildoor a login can be granted. They are the feature
// segments of the token's "services" claim for the configured service name
// (--auth-service, default "buildoor"): "+buildoor:plan" grants FeaturePlan,
// "+buildoor:all" every feature. All of them are default-deny: a login
// without the grant (or without a "services" claim) has basic access only.
//
// These are names only. Which feature an endpoint needs is stated by its
// handler, in the Require call at its top, and nowhere else.
const (
	// FeatureBasic is base access: a valid login the service gate lets
	// through. It covers the private reads (pool contents, unredacted plans
	// and payloads, the audit log, profiles) and the actions that change no
	// state (local build probe, transform dry run, /rpc submissions).
	FeatureBasic = auth.FeatureBasic
	// FeaturePlan allows editing the per-slot action plans and the recurring
	// plan rules.
	FeaturePlan = "plan"
	// FeatureTxPool allows dropping transactions from the transaction pool.
	FeatureTxPool = "txpool"
	// FeatureConfig allows changing the settings.
	FeatureConfig = "config"
	// FeatureRPC allows reading the /rpc ingress access state (its caller
	// stats carry client IPs) and managing the ingress API keys.
	FeatureRPC = "rpc"
	// FeatureKeys allows the builder key lifecycle: the target key count,
	// deposits, top-ups and exits.
	FeatureKeys = "keys"
)

// Features lists the grantable features (FeatureBasic is not one: every
// login has it).
var Features = []string{FeaturePlan, FeatureTxPool, FeatureConfig, FeatureRPC, FeatureKeys}
