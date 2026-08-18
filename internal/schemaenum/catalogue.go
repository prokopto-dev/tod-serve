package schemaenum

import (
	"errors"
	"fmt"
	"strings"
)

// Enum names, so a caller looking one up spells it once. The name is the qualified column —
// `table.column` — except for `server`, which is a type used by several tables rather than one
// column, and is named that way in the canonical conventions.
const (
	NameServer                   = "server"
	NameCircleState              = "circle.state"
	NameMembershipRole           = "membership.role"
	NameMembershipKind           = "membership.kind"
	NameInviteMintedByKind       = "invite.minted_by_kind"
	NameIdentityProviderKind     = "identity_provider.kind"
	NameTodReportKind            = "tod_report.kind"
	NameTodReportSource          = "tod_report.source"
	NameTodReportSelfConfidence  = "tod_report.self_confidence"
	NameTargetStateStatus        = "target_state.status"
	NameTargetStateConfidence    = "target_state.confidence"
	NameTargetStateContestReason = "target_state.contest_reason"
	NameTargetStateChangeReason  = "target_state.change_reason"
	NameRaidTargetExpansion      = "raid_target.expansion"
	NameRaidTargetCategory       = "raid_target.category"
	NameRaidTargetState          = "raid_target.state"
	NameRaidTargetTimerWindow    = "raid_target_timer.window_kind"
	NameCircleRevocationStrength = "circle.revocation_strength"
	NameIdentityLinkMethod       = "identity_link.method"
)

// The values themselves. Untyped constants on purpose: a domain package declares its own type
// (core.Server, authz.Role) with these as the initialisers, so the string appears once in the
// repository and the typed constant still costs nothing.
const (
	ServerBlue  = "blue"
	ServerGreen = "green"
	ServerRed   = "red"

	CircleStateActive   = "active"
	CircleStateArchived = "archived"

	MembershipRoleOwner    = "owner"
	MembershipRoleOfficer  = "officer"
	MembershipRoleMember   = "member"
	MembershipRoleObserver = "observer"

	MembershipKindHuman   = "human"
	MembershipKindService = "service"

	InviteMintedByKindSession = "session"
	InviteMintedByKindPAT     = "pat"

	IdentityProviderKindDiscord = "discord"
	IdentityProviderKindOIDC    = "oidc"
	IdentityProviderKindLocal   = "local"

	TodReportKindKill       = "kill"
	TodReportKindRetraction = "retraction"

	TodReportSourceLogLine = "log_line"
	TodReportSourceManual  = "manual"
	TodReportSourceAPI     = "api"
	TodReportSourceImport  = "import"

	TodReportSelfConfidenceCertain  = "certain"
	TodReportSelfConfidenceProbable = "probable"
	TodReportSelfConfidenceGuess    = "guess"

	TargetStateStatusUnknown   = "unknown"
	TargetStateStatusNoTimer   = "no_timer"
	TargetStateStatusPreWindow = "pre_window"
	TargetStateStatusInWindow  = "in_window"
	TargetStateStatusOverdue   = "overdue"
	TargetStateStatusUp        = "up"

	TargetStateConfidenceUnknown = "unknown"
	TargetStateConfidenceLow     = "low"
	TargetStateConfidenceMedium  = "medium"
	TargetStateConfidenceHigh    = "high"

	TargetStateContestReasonThinSupersede       = "thin_supersede"
	TargetStateContestReasonImplausibleOrdering = "implausible_ordering"
	TargetStateContestReasonWideSpread          = "wide_spread"
	TargetStateContestReasonPendingSupersede    = "pending_supersede"

	TargetStateChangeReasonNewKill       = "new_kill"
	TargetStateChangeReasonCorroboration = "corroboration"
	TargetStateChangeReasonRetraction    = "retraction"
	TargetStateChangeReasonQuake         = "quake"
	TargetStateChangeReasonTimerChange   = "timer_change"

	RaidTargetExpansionClassic = "classic"
	RaidTargetExpansionKunark  = "kunark"
	RaidTargetExpansionVelious = "velious"

	RaidTargetCategoryOpenWorld = "open_world"
	RaidTargetCategoryZoneBoss  = "zone_boss"
	RaidTargetCategoryPlanar    = "planar"
	RaidTargetCategoryNToV      = "ntov"
	RaidTargetCategorySleeper   = "sleeper"
	RaidTargetCategoryKeyHolder = "key_holder"

	RaidTargetStateActive  = "active"
	RaidTargetStateRetired = "retired"

	RaidTargetTimerWindowKindFixed    = "fixed"
	RaidTargetTimerWindowKindVariance = "variance"
	RaidTargetTimerWindowKindUnknown  = "unknown"

	CircleRevocationStrengthDurable = "durable"
	CircleRevocationStrengthWeak    = "weak"

	IdentityLinkMethodOfficerAsserted  = "officer_asserted"
	IdentityLinkMethodProviderVerified = "provider_verified"
)

// All returns the catalogue, in the order canonical conventions §5 lists it.
//
// It builds the slice on every call rather than sharing one: a package-level slice is mutable, and
// a caller that sorts or appends to the catalogue in place would change what every later caller
// sees, silently and from a distance.
func All() []Enum {
	return []Enum{
		{Name: NameServer, Values: []string{ServerBlue, ServerGreen, ServerRed}},
		{Name: NameCircleState, Values: []string{CircleStateActive, CircleStateArchived}},
		{
			Name: NameMembershipRole,
			Values: []string{
				MembershipRoleOwner, MembershipRoleOfficer,
				MembershipRoleMember, MembershipRoleObserver,
			},
			// Listed strongest first, which is the reverse of the ordering rule
			// `observer < member < officer < owner`. The direction is recorded rather than the
			// list reordered, because the list must match the canonical document element by
			// element and the document reads best strongest-first.
			Order: Descending,
		},
		{Name: NameMembershipKind, Values: []string{MembershipKindHuman, MembershipKindService}},
		{
			Name:   NameInviteMintedByKind,
			Values: []string{InviteMintedByKindSession, InviteMintedByKindPAT},
		},
		{
			Name: NameIdentityProviderKind,
			Values: []string{
				IdentityProviderKindDiscord, IdentityProviderKindOIDC, IdentityProviderKindLocal,
			},
		},
		{Name: NameTodReportKind, Values: []string{TodReportKindKill, TodReportKindRetraction}},
		{
			Name: NameTodReportSource,
			Values: []string{
				TodReportSourceLogLine, TodReportSourceManual,
				TodReportSourceAPI, TodReportSourceImport,
			},
		},
		{
			Name: NameTodReportSelfConfidence,
			Values: []string{
				TodReportSelfConfidenceCertain, TodReportSelfConfidenceProbable,
				TodReportSelfConfidenceGuess,
			},
		},
		{
			Name: NameTargetStateStatus,
			Values: []string{
				TargetStateStatusUnknown, TargetStateStatusNoTimer, TargetStateStatusPreWindow,
				TargetStateStatusInWindow, TargetStateStatusOverdue, TargetStateStatusUp,
			},
		},
		{
			Name: NameTargetStateConfidence,
			Values: []string{
				TargetStateConfidenceUnknown, TargetStateConfidenceLow,
				TargetStateConfidenceMedium, TargetStateConfidenceHigh,
			},
			// `unknown < low < medium < high`. Ordered because "at least medium" is a real
			// question and a float would be read as a probability we cannot compute.
			Order: Ascending,
		},
		{
			Name: NameTargetStateContestReason,
			Values: []string{
				TargetStateContestReasonThinSupersede, TargetStateContestReasonImplausibleOrdering,
				TargetStateContestReasonWideSpread, TargetStateContestReasonPendingSupersede,
			},
		},
		{
			Name: NameTargetStateChangeReason,
			Values: []string{
				TargetStateChangeReasonNewKill, TargetStateChangeReasonCorroboration,
				TargetStateChangeReasonRetraction, TargetStateChangeReasonQuake,
				TargetStateChangeReasonTimerChange,
			},
		},
		{
			Name: NameRaidTargetExpansion,
			Values: []string{
				RaidTargetExpansionClassic, RaidTargetExpansionKunark, RaidTargetExpansionVelious,
			},
		},
		{
			Name: NameRaidTargetCategory,
			Values: []string{
				RaidTargetCategoryOpenWorld, RaidTargetCategoryZoneBoss, RaidTargetCategoryPlanar,
				RaidTargetCategoryNToV, RaidTargetCategorySleeper, RaidTargetCategoryKeyHolder,
			},
		},
		{Name: NameRaidTargetState, Values: []string{RaidTargetStateActive, RaidTargetStateRetired}},
		{
			Name: NameRaidTargetTimerWindow,
			Values: []string{
				RaidTargetTimerWindowKindFixed, RaidTargetTimerWindowKindVariance,
				RaidTargetTimerWindowKindUnknown,
			},
		},
		{
			Name:   NameCircleRevocationStrength,
			Values: []string{CircleRevocationStrengthDurable, CircleRevocationStrengthWeak},
		},
		{
			Name: NameIdentityLinkMethod,
			Values: []string{
				IdentityLinkMethodOfficerAsserted, IdentityLinkMethodProviderVerified,
			},
		},
	}
}

// Lookup returns the enum with the given name. The bool is false rather than a zero Enum being
// returned silently, because a caller that misspells a name would otherwise generate a CHECK
// constraint permitting nothing at all.
func Lookup(name string) (Enum, bool) {
	for _, e := range All() {
		if e.Name == name {
			return e, true
		}
	}
	return Enum{}, false
}

// ErrUnordered is returned by ordering operations on an enum that declares no order. Inventing one
// would put a second, invisible ordering rule in the codebase.
var ErrUnordered = errors.New("enum declares no order")

// Order says how to read [Enum.Values]: as a list with no meaning to its sequence, or as the
// ordering rule itself, running up or down.
type Order uint8

// The three orders. Unordered is the zero value, so an enum that says nothing about order has none.
const (
	Unordered Order = iota
	Ascending
	Descending
)

// String renders the order for generated SQL comments and test failures.
func (o Order) String() string {
	switch o {
	case Ascending:
		return "ascending"
	case Descending:
		return "descending"
	case Unordered:
		return "unordered"
	default:
		return fmt.Sprintf("Order(%d)", uint8(o))
	}
}

// Enum is one enumerated column: the values it may hold, and whether those values are ordered.
type Enum struct {
	// Name is the qualified column, `table.column`.
	Name string
	// Values are the permitted values, listed exactly as canonical conventions §5 lists them.
	Values []string
	// Order says whether Values is a ranking and which way it runs.
	Order Order
}

// Contains reports whether value is permitted.
func (e Enum) Contains(value string) bool {
	for _, v := range e.Values {
		if v == value {
			return true
		}
	}
	return false
}

// Rank returns the ascending position of value — 0 is the weakest — and whether it has one. It is
// the only place that knows which way [Enum.Values] runs, so "is this role at least an officer"
// has one answer in the codebase and not one per caller.
func (e Enum) Rank(value string) (int, bool) {
	for i, v := range e.Values {
		if v != value {
			continue
		}
		if e.Order == Descending {
			return len(e.Values) - 1 - i, true
		}
		return i, e.Order == Ascending
	}
	return 0, false
}

// InSQL renders the `column IN ('a', 'b')` predicate for a column holding this enum. Values are
// known-safe — a test asserts every value is lowercase snake_case — so the quoting here is not a
// sanitiser and must not be mistaken for one.
//
// It is separate from [Enum.CheckSQL] because Atlas HCL takes the bare expression while a migration
// takes the whole constraint, and rendering the value list twice is how the two would drift.
func (e Enum) InSQL(column string) string {
	quoted := make([]string, 0, len(e.Values))
	for _, v := range e.Values {
		quoted = append(quoted, "'"+v+"'")
	}
	return fmt.Sprintf("%s IN (%s)", column, strings.Join(quoted, ", "))
}

// CheckSQL renders the CHECK constraint for a column holding this enum.
func (e Enum) CheckSQL(column string) string {
	return fmt.Sprintf("CHECK (%s)", e.InSQL(column))
}

// OrderBySQL renders the `CASE … END` that sorts a column by this enum's ranking, ascending.
// Storing the TEXT value keeps `sqlite3 tod.db` readable, which is the officer's real debugging
// tool; the cost is that ordering has to be spelled out, and this is where it is spelled.
func (e Enum) OrderBySQL(column string) (string, error) {
	if e.Order == Unordered {
		return "", fmt.Errorf("order by %s: %w", e.Name, ErrUnordered)
	}
	var b strings.Builder
	b.WriteString("CASE " + column)
	for _, v := range e.Values {
		rank, ok := e.Rank(v)
		if !ok {
			return "", fmt.Errorf("rank %s.%s: %w", e.Name, v, ErrUnordered)
		}
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", v, rank)
	}
	b.WriteString(" END")
	return b.String(), nil
}
