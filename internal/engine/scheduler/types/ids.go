// Package types holds leaf value types shared across scheduler subpackages.
// Zero reverse dependencies; only imports stdlib.
package types

// TaskID identifies a task. Always prefixed "t-" by convention.
type TaskID string

// TeamID identifies a tenant team (multi-tenant placeholder for phase N).
type TeamID string

// Ref points to a blob in some object store. Reserved field; phase 1 not yet used, left for future.
type Ref string

// MetaRef points to a meta.json relative to sessionRoot.
//   react flat:   "meta.json"
//   plan per-task:"tasks/<tid>/meta.json"
type MetaRef string
