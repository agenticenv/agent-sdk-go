package actionpolicy

import (
	"context"
	"sync"
)

// SessionStore keeps one [SessionState] per conversation so later runs continue the chain.
// Load returns nil when id has no state. Save returns [ErrSessionConflict] when prevHead
// is not the head currently stored. Pass the head returned by the matching Load.
// Two runs of one conversation cannot commit together.
type SessionStore interface {
	Load(ctx context.Context, id string) (*SessionState, error)
	Save(ctx context.Context, id string, prevHead string, state *SessionState) error
}

// MemorySessionStore is an in-process [SessionStore].
type MemorySessionStore struct {
	mu     sync.Mutex
	states map[string]SessionState
}

// NewMemorySessionStore returns an empty store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{states: make(map[string]SessionState)}
}

// Load returns a copy of the state for id, or nil when none exists.
func (s *MemorySessionStore) Load(_ context.Context, id string) (*SessionState, error) {
	if id == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[id]
	if !ok {
		return nil, nil
	}
	copied := cloneState(state)
	return &copied, nil
}

// Save stores state when the current head equals prevHead.
func (s *MemorySessionStore) Save(_ context.Context, id string, prevHead string, state *SessionState) error {
	if id == "" || state == nil {
		return ErrSessionConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.states[id]
	head := ""
	if ok {
		head = current.Head
	}
	if head != prevHead {
		return ErrSessionConflict
	}
	s.states[id] = cloneState(*state)
	return nil
}

func cloneState(state SessionState) SessionState {
	state.Labels = copyStrings(state.Labels)
	if len(state.Recent) == 0 {
		state.Recent = nil
		return state
	}
	recent := make([]LedgerEntry, len(state.Recent))
	for i, entry := range state.Recent {
		entry.Labels = copyStrings(entry.Labels)
		recent[i] = entry
	}
	state.Recent = recent
	return state
}
