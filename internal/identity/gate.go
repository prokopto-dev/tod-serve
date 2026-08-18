package identity

import (
	"errors"

	"github.com/prokopto-dev/tod-serve/internal/identity/discord"
)

// GuildGate is the per-circle Discord gate, re-exported so a caller in internal/circle or
// internal/api takes one type rather than reaching into the provider package for it.
type GuildGate = discord.Gate

// GuildFacts is what the `credential_ticket` carries, likewise re-exported.
type GuildFacts = discord.GuildFacts

// EvaluateGuildGate decides whether a verified subject may enter, and returns a coded error when
// they may not.
//
// This is the reusable half: it is called from `/join` AND from `/sessions`, against the facts on
// the 120-second `credential_ticket` — never against a cached copy and never against a
// client-supplied claim. A gate on join alone would let `/sessions` mint a fresh PAT for somebody
// who has left the guild.
//
// The three failures are three different codes because they have three different fixes:
//
//	guild_membership_required   join the guild
//	guild_role_required         ask an officer for the role — or we hold no fact and will not guess
//	provider_scope_declined     re-authorize and grant the permission
//
// Absent facts return `guild_role_required`, not success. Reading an absent role list as an empty
// one would disable the gate for every user while appearing to enforce it.
func EvaluateGuildGate(gate GuildGate, facts GuildFacts) error {
	// scopeDeclined is false here because a declined scope never reaches this point: the callback
	// refuses to mint a ticket for one, and the bearer_token path fails at verification. The
	// parameter stays in internal/identity/discord, where the case is reachable and tested.
	err := discord.EvaluateGate(gate, facts, false)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, discord.ErrGuildMembershipRequired):
		return NewError(CodeGuildMembershipRequired, "this circle requires membership of a Discord guild you are not in", err)
	case errors.Is(err, discord.ErrScopeDeclined):
		return NewError(CodeProviderScopeDeclined, "this circle's guild check needs a permission that was not granted", err)
	default:
		return NewError(CodeGuildRoleRequired, "this circle requires a Discord role, and we hold no fact that you have one", err)
	}
}
