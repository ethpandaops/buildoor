// What the current login may do in the UI: the features of buildoor a login
// can be granted through the "services" claim of its token (e.g.
// "+buildoor:plan", "+buildoor:all"; see the authentication docs).
//
// The claim is not evaluated here. The authenticatoor client the page loads
// (window.ethpandaops.authenticatoor) answers hasFeature(service, feature)
// for the current session; stores/authStore.ts asks it on every session
// change and keeps the answers in its state (see useFeatures). They only
// decide which controls the UI shows or disables: the API verifies the token
// and answers 403 for a missing feature, whatever the UI shows.

import { getRuntimeConfig } from './runtimeConfig';

// The features of buildoor a login can be granted (the Feature* constants of
// pkg/webui/handlers/auth/features.go). All are default-deny.
export const FEATURES = ['plan', 'txpool', 'config', 'rpc', 'keys'] as const;

export type Feature = (typeof FEATURES)[number];

// Whether the current login has each feature.
export type FeatureSet = Readonly<Record<Feature, boolean>>;

const featureSet = (value: boolean): FeatureSet =>
  Object.freeze(Object.fromEntries(FEATURES.map((feature) => [feature, value])) as Record<Feature, boolean>);

// Open mode (no auth provider): the server allows everything.
export const ALL_FEATURES = featureSet(true);

// No login, or a login without any grant.
export const NO_FEATURES = featureSet(false);

// The name this instance has in a "services" claim (--auth-service).
export function authService(): string {
  return getRuntimeConfig().authService;
}

// The feature as a grant names it, e.g. "buildoor:keys".
export function featureName(feature: Feature): string {
  return `${authService()}:${feature}`;
}

// The tooltip of a control the login lacks the feature for.
export function needsFeature(feature: Feature): string {
  return `Needs the feature ${featureName(feature)}`;
}

export function sameFeatures(a: FeatureSet, b: FeatureSet): boolean {
  return FEATURES.every((feature) => a[feature] === b[feature]);
}
