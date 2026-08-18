package discord

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The gate's failure modes. Three, not two, because "we could not look" is a different fact from
// "we looked and you are not in it", and they point at completely different fixes.
var (
	// ErrGuildMembershipRequired is Discord's 404: the subject is not in the gated guild.
	ErrGuildMembershipRequired = errors.New("the subject is not in the guild this circle requires")

	// ErrGuildRoleRequired is the subject holding none of the required roles — and also the
	// subject about whom no fact exists at all. See [EvaluateGate] for why absent rejects.
	ErrGuildRoleRequired = errors.New("the subject holds none of the roles this circle requires")
)

// Gate is the per-circle Discord access gate, from `circle_provider`.
//
// The instance owns the application; the CIRCLE owns the gate. Two circles on one instance may
// point at two different guilds, which is why this is not an instance setting.
type Gate struct {
	// GuildID is the guild membership of which this circle requires. Empty means no guild gate.
	GuildID string

	// RequiredRoleIDs narrows the gate from "anyone in the guild" to "anyone in the guild holding
	// one of these roles". EMPTY MEANS ANYONE IN THE GUILD.
	//
	// Holding ANY listed role admits. The list widens who gets in as it grows, which is what
	// makes "empty means anyone" the same rule with the list at its most permissive rather than a
	// special case bolted on the front. Requiring all of them would mean a circle naming
	// "Raider" and "Officer" admitted only the people who are both, which is nobody, and the
	// failure would look exactly like a broken gate.
	RequiredRoleIDs []string
}

// IsZero reports whether this circle gates on a guild at all.
func (g Gate) IsZero() bool { return g.GuildID == "" }

// EvaluateGate decides whether facts satisfy g.
//
// The rule that matters is the third case: **an absent fact rejects, it never skips.** No member
// object means no evaluation, and no evaluation means no entry. An implementation that read an
// absent role list as an empty one would disable the gate for every user while appearing to
// enforce it, which is precisely the confident mistake this project is built against.
//
// scopeDeclined is passed separately rather than inferred from the absence of facts because the
// two have different fixes: grant the permission, versus go and ask an officer for a role you may
// already hold.
func EvaluateGate(g Gate, facts GuildFacts, scopeDeclined bool) error {
	if g.IsZero() {
		return nil
	}
	if scopeDeclined {
		return fmt.Errorf("%s: %w", ScopeGuildsMembersRead, ErrScopeDeclined)
	}

	fact, known := facts[g.GuildID]
	if !known {
		return fmt.Errorf("no member object for guild %s: %w", g.GuildID, ErrGuildRoleRequired)
	}
	if !fact.Member {
		return fmt.Errorf("guild %s: %w", g.GuildID, ErrGuildMembershipRequired)
	}
	if len(g.RequiredRoleIDs) == 0 {
		return nil
	}
	held := make(map[string]struct{}, len(fact.RoleIDs))
	for _, r := range fact.RoleIDs {
		held[r] = struct{}{}
	}
	for _, want := range g.RequiredRoleIDs {
		if _, ok := held[want]; ok {
			return nil
		}
	}
	return fmt.Errorf("guild %s: %w", g.GuildID, ErrGuildRoleRequired)
}

// ParseRoleIDs reads `circle_provider.discord_required_role_ids_json`.
//
// An empty column is an empty list, which means "anyone in the guild". A column that does not
// parse is an error rather than an empty list: the schema's `json_valid` CHECK makes that
// unreachable, and treating it as "no roles required" if it ever happened would open the gate.
func ParseRoleIDs(raw string) ([]string, error) {
	if raw == "" {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("parse discord_required_role_ids_json: %w", err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// MarshalFacts renders the facts for `credential_ticket.guild_roles_json`.
func MarshalFacts(facts GuildFacts) (string, error) {
	if facts == nil {
		facts = GuildFacts{}
	}
	b, err := json.Marshal(facts)
	if err != nil {
		return "", fmt.Errorf("marshal guild facts: %w", err)
	}
	return string(b), nil
}

// ParseFacts reads the facts back off a `credential_ticket`.
//
// A column that does not parse is an error, for the same reason ParseRoleIDs refuses one: the
// alternative is an empty fact set, and an empty fact set now rejects — but relying on that is
// relying on a distant behaviour to make a local shortcut safe.
func ParseFacts(raw string) (GuildFacts, error) {
	if raw == "" {
		return GuildFacts{}, nil
	}
	facts := GuildFacts{}
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return nil, fmt.Errorf("parse guild_roles_json: %w", err)
	}
	return facts, nil
}
