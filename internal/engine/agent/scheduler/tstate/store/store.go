// Package store provides the tstate.Store implementation(s).
// The Store, Tx, and Mutation interfaces/types live in the parent tstate package
// to avoid import cycles (tstate/store imports tstate for TaskState).
// This file re-exports them as type aliases so existing callers (e.g. tests)
// can continue using store.Mutation / store.Tx / store.Store unchanged.
package store

import "harnessclaw-go/internal/engine/agent/scheduler/tstate"

// Store is an alias for tstate.Store.
type Store = tstate.Store

// Tx is an alias for tstate.Tx.
type Tx = tstate.Tx

// Mutation is an alias for tstate.Mutation.
type Mutation = tstate.Mutation
