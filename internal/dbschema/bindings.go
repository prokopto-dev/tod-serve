package dbschema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// EnumsHCLPath is the generated Atlas file, relative to the repository root.
const EnumsHCLPath = "db/enums.hcl"

// RegenerateCommand is how the generated file is rewritten. It appears in the file itself, because
// the first person to find a mistake in a generated file otherwise fixes the file.
const RegenerateCommand = "make gen"

// Binding is one column whose permitted values come from the enum catalogue.
type Binding struct {
	// Table is the SQL table, singular snake_case.
	Table string
	// Column is the SQL column.
	Column string
	// Enum is the catalogue name, as internal/schemaenum spells it.
	Enum string
}

// LocalName is the Atlas local this binding's predicate is rendered into, and the name
// `db/schema.hcl` refers to. Derived from the table and column so that a schema author who wants
// the constraint on `tod_report.kind` writes `local.check_tod_report_kind` and cannot accidentally
// name the one belonging to another column.
func (b Binding) LocalName() string {
	return fmt.Sprintf("check_%s_%s", b.Table, b.Column)
}

// Predicate renders the `column IN (…)` expression for this binding.
func (b Binding) Predicate() (string, error) {
	enum, ok := schemaenum.Lookup(b.Enum)
	if !ok {
		return "", fmt.Errorf("bind %s.%s: no enum named %q in the catalogue",
			b.Table, b.Column, b.Enum)
	}
	return enum.InSQL(b.Column), nil
}

// Bindings returns every enum-constrained column in the schema, in table order.
//
// Most bindings are the identity mapping — the catalogue names an enum `tod_report.kind` and the
// column is `tod_report.kind`. The ones that are not carry a comment, because a reader checking
// whether the schema honours canonical §5 needs to know which mismatches are deliberate.
func Bindings() []Binding {
	return []Binding{
		// `server` is a type used by several tables rather than one column, which is why the
		// catalogue names it without a table. A circle is pinned to one server; a catalogue timer
		// is per server. Nothing else stores one — a report's `server` is echoed in the request
		// body and checked against the circle's, never written (domain model, `tod_report`).
		{Table: "circle", Column: "server", Enum: schemaenum.NameServer},
		{Table: "raid_target_timer", Column: "server", Enum: schemaenum.NameServer},

		{Table: "circle", Column: "state", Enum: schemaenum.NameCircleState},

		{Table: "circle_timer_override", Column: "window_kind", Enum: schemaenum.NameRaidTargetTimerWindow},

		{Table: "identity_link", Column: "method", Enum: schemaenum.NameIdentityLinkMethod},
		{Table: "identity_provider", Column: "kind", Enum: schemaenum.NameIdentityProviderKind},

		// An invite grants a role, so it draws on the membership role enum; `CHECK (role <> 'owner')`
		// sits next to it in the DDL and is shape rather than catalogue.
		{Table: "invite", Column: "role", Enum: schemaenum.NameMembershipRole},
		{Table: "invite", Column: "minted_by_kind", Enum: schemaenum.NameInviteMintedByKind},

		{Table: "membership", Column: "kind", Enum: schemaenum.NameMembershipKind},
		{Table: "membership", Column: "role", Enum: schemaenum.NameMembershipRole},

		// A quake is reported through the same channels a kill is, so it carries the same source
		// enum. `log_line` is unreachable for one today; a narrower copy of the list would be a
		// second enum for one concept, which is what canonical §5 exists to prevent.
		{Table: "quake_event", Column: "source", Enum: schemaenum.NameTodReportSource},

		{Table: "raid_target", Column: "category", Enum: schemaenum.NameRaidTargetCategory},
		{Table: "raid_target", Column: "expansion", Enum: schemaenum.NameRaidTargetExpansion},
		{Table: "raid_target", Column: "state", Enum: schemaenum.NameRaidTargetState},

		{Table: "raid_target_timer", Column: "window_kind", Enum: schemaenum.NameRaidTargetTimerWindow},

		// The catalogue names these `target_state.*` — the resource the API returns — while the
		// table is `target_state_cache`, because the table is a droppable cache of that resource
		// and never its authority.
		{Table: "target_state_cache", Column: "change_reason", Enum: schemaenum.NameTargetStateChangeReason},
		{Table: "target_state_cache", Column: "confidence", Enum: schemaenum.NameTargetStateConfidence},
		{Table: "target_state_cache", Column: "contest_reason", Enum: schemaenum.NameTargetStateContestReason},
		{Table: "target_state_cache", Column: "status", Enum: schemaenum.NameTargetStateStatus},

		{Table: "tod_report", Column: "kind", Enum: schemaenum.NameTodReportKind},
		{Table: "tod_report", Column: "self_confidence", Enum: schemaenum.NameTodReportSelfConfidence},
		{Table: "tod_report", Column: "source", Enum: schemaenum.NameTodReportSource},
	}
}

// UnstoredEnums names the catalogue enums that no column holds, and says why.
//
// It exists so that TestEnumBindings_EveryCatalogueEnum_IsBoundOrExplicitlyUnstored can be a
// two-sided check. Without it, adding an enum to the catalogue and forgetting the column would
// look exactly like an enum that is deliberately derived, and the schema would quietly stop
// covering §5.
func UnstoredEnums() map[string]string {
	return map[string]string{
		// Derived from the circle's accepted providers on every read. The domain model is explicit:
		// storing it would let it drift the moment a provider is added to the instance.
		schemaenum.NameCircleRevocationStrength: "derived from the accepted providers, never stored",
	}
}

// EnumsHCL renders db/enums.hcl: one Atlas local per binding, holding the `IN (…)` predicate.
//
// Locals rather than a generated `table` block, because the shape of a table is a human decision
// and belongs in a file a human reviews. Only the value list is generated, and it is generated
// where it cannot be edited by accident.
func EnumsHCL() (string, error) {
	bindings := Bindings()
	sorted := make([]Binding, len(bindings))
	copy(sorted, bindings)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LocalName() < sorted[j].LocalName() })

	var b strings.Builder
	b.WriteString("// Generated by internal/dbschema from the enum catalogue in internal/schemaenum.\n")
	fmt.Fprintf(&b, "// Do not edit: regenerate with `%s`.\n", RegenerateCommand)
	b.WriteString("//\n")
	b.WriteString("// Canonical conventions §5: the wire value is the database value, and both the\n")
	b.WriteString("// SQL CHECK and the OpenAPI enum come from one Go catalogue. db/schema.hcl refers\n")
	b.WriteString("// to these by name so that no value list is written twice.\n")
	b.WriteString("\nlocals {\n")

	width := 0
	for _, bind := range sorted {
		if n := len(bind.LocalName()); n > width {
			width = n
		}
	}
	for _, bind := range sorted {
		predicate, err := bind.Predicate()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  %-*s = %q\n", width, bind.LocalName(), predicate)
	}
	b.WriteString("}\n")
	return b.String(), nil
}
