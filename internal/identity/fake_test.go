package identity_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/identity"
)

// fakeStore is an in-memory identity.Store that RECORDS every call, arguments included.
//
// The recording is the point. "A Discord access token is never persisted" is an invariant, and
// the only way to assert it rather than review for it is to be able to say "no store call
// received this string" — which needs a store that remembers what it was passed.
type fakeStore struct {
	mu sync.Mutex

	providersByKey map[string]identity.Provider
	providersByID  map[string]identity.Provider
	flows          map[string]identity.AuthFlow
	flowsConsumed  map[string]bool
	tickets        map[string]identity.Ticket
	ticketsUsed    map[string]bool
	invites        map[string]identity.Invite
	gates          map[string]identity.GuildGate
	anyGuildGate   bool
	identities     map[string]identity.StoredIdentity
	circlesFor     map[string][]string

	// calls is every argument every method received, rendered. Rendered rather than kept as
	// values so one assertion can search all of them for a secret regardless of its type.
	calls []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		providersByKey: map[string]identity.Provider{},
		providersByID:  map[string]identity.Provider{},
		flows:          map[string]identity.AuthFlow{},
		flowsConsumed:  map[string]bool{},
		tickets:        map[string]identity.Ticket{},
		ticketsUsed:    map[string]bool{},
		invites:        map[string]identity.Invite{},
		gates:          map[string]identity.GuildGate{},
		identities:     map[string]identity.StoredIdentity{},
		circlesFor:     map[string][]string{},
	}
}

func (f *fakeStore) record(method string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s%+v", method, args))
}

// recorded returns every call, rendered.
func (f *fakeStore) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeStore) addProvider(p identity.Provider) {
	f.providersByKey[p.Key] = p
	f.providersByID[p.ID] = p
}

func (f *fakeStore) ProviderByKey(_ context.Context, key string) (identity.Provider, error) {
	f.record("ProviderByKey", key)
	p, ok := f.providersByKey[key]
	if !ok {
		return identity.Provider{}, identity.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) ProviderByID(_ context.Context, id string) (identity.Provider, error) {
	f.record("ProviderByID", id)
	p, ok := f.providersByID[id]
	if !ok {
		return identity.Provider{}, identity.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) EnabledProviders(context.Context) ([]identity.Provider, error) {
	f.record("EnabledProviders")
	var out []identity.Provider
	for _, p := range f.providersByKey {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateAuthFlow(_ context.Context, flow identity.AuthFlow) error {
	f.record("CreateAuthFlow", flow)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows[flow.State] = flow
	return nil
}

// ConsumeAuthFlow mirrors the real query's `WHERE consumed_at IS NULL`: a second consumption
// returns no row, which is what makes a replayed callback a dead end.
func (f *fakeStore) ConsumeAuthFlow(_ context.Context, state string, at core.Micros) (identity.AuthFlow, error) {
	f.record("ConsumeAuthFlow", state, at)
	f.mu.Lock()
	defer f.mu.Unlock()
	flow, ok := f.flows[state]
	if !ok || f.flowsConsumed[state] {
		return identity.AuthFlow{}, identity.ErrNotFound
	}
	f.flowsConsumed[state] = true
	return flow, nil
}

func (f *fakeStore) CreateTicket(_ context.Context, ticket identity.Ticket) error {
	f.record("CreateTicket", ticket)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tickets[hex.EncodeToString(ticket.Hash)] = ticket
	return nil
}

func (f *fakeStore) ReadTicket(_ context.Context, hash []byte) (identity.Ticket, error) {
	f.record("ReadTicket", hex.EncodeToString(hash))
	f.mu.Lock()
	defer f.mu.Unlock()
	key := hex.EncodeToString(hash)
	ticket, ok := f.tickets[key]
	if !ok || f.ticketsUsed[key] {
		return identity.Ticket{}, identity.ErrNotFound
	}
	return ticket, nil
}

// ConsumeTicket mirrors `WHERE consumed_at IS NULL` plus trg_credential_ticket_single_use: the
// second call finds nothing, and in the real database a write that tried anyway would abort.
func (f *fakeStore) ConsumeTicket(_ context.Context, hash []byte, at core.Micros) (identity.Ticket, error) {
	f.record("ConsumeTicket", hex.EncodeToString(hash), at)
	f.mu.Lock()
	defer f.mu.Unlock()
	key := hex.EncodeToString(hash)
	ticket, ok := f.tickets[key]
	if !ok || f.ticketsUsed[key] {
		return identity.Ticket{}, identity.ErrNotFound
	}
	f.ticketsUsed[key] = true
	return ticket, nil
}

func (f *fakeStore) InviteByCode(_ context.Context, code string) (identity.Invite, error) {
	f.record("InviteByCode", code)
	invite, ok := f.invites[code]
	if !ok {
		return identity.Invite{}, identity.ErrNotFound
	}
	return invite, nil
}

func (f *fakeStore) InviteByCodeHash(_ context.Context, hash []byte) (identity.Invite, error) {
	f.record("InviteByCodeHash", hex.EncodeToString(hash))
	for _, invite := range f.invites {
		if hex.EncodeToString(invite.CodeHash) == hex.EncodeToString(hash) {
			return invite, nil
		}
	}
	return identity.Invite{}, identity.ErrNotFound
}

func (f *fakeStore) GuildGate(_ context.Context, circleID, providerID string) (identity.GuildGate, error) {
	f.record("GuildGate", circleID, providerID)
	gate, ok := f.gates[circleID+"/"+providerID]
	if !ok {
		return identity.GuildGate{}, identity.ErrNotFound
	}
	return gate, nil
}

func (f *fakeStore) AnyCircleGatesOnAGuild(context.Context) (bool, error) {
	f.record("AnyCircleGatesOnAGuild")
	return f.anyGuildGate, nil
}

func (f *fakeStore) CircleIDsForIdentity(_ context.Context, identityID string) ([]string, error) {
	f.record("CircleIDsForIdentity", identityID)
	return f.circlesFor[identityID], nil
}

func (f *fakeStore) IdentityBySubject(_ context.Context, providerID, subject string) (identity.StoredIdentity, error) {
	f.record("IdentityBySubject", providerID, subject)
	stored, ok := f.identities[providerID+"/"+subject]
	if !ok {
		return identity.StoredIdentity{}, identity.ErrNotFound
	}
	return stored, nil
}

// countingEntropy is a deterministic entropy source, so a test can name the state and the ticket
// it expects. crypto/rand would work and would make every assertion "something changed".
type countingEntropy struct {
	mu sync.Mutex
	n  byte
}

func (e *countingEntropy) Read(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	for i := range p {
		p[i] = e.n
	}
	return len(p), nil
}

var _ identity.Store = (*fakeStore)(nil)
