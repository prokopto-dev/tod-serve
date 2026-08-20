package catalogue

import (
	"context"
	"log/slog"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
	"github.com/prokopto-dev/tod-serve/internal/store"
	"github.com/prokopto-dev/tod-serve/internal/store/sqlitegen"
)

// EmbeddedTarget is one raid target's identity as this repository ships it.
//
// Identity ships and timers do not, and the line between them is a licence boundary rather than a
// packaging preference — canonical §15. A mob's name, its zone, which expansion it belongs to and
// what kind of encounter it is are facts about the game, checkable by anybody who has played it.
// Its respawn window is a community measurement, disputed, and not ours to redistribute.
//
// There is deliberately no timer field on this struct. Adding one would not merely be wrong; it
// would be the change SEED001 exists to catch.
type EmbeddedTarget struct {
	Name      string
	Zone      string
	Expansion string
	Category  string
	// Aliases are the spellings raiders actually type. They are the working half of the resolve
	// ladder: `Naggy` and `VS` are what a plugin sees in a log line and in raid chat, and without
	// them every short form falls through to the substring rung where it can tie.
	Aliases []string
	// QuakeImmune marks a target a server-wide repop does NOT reset.
	//
	// Spelled negatively because almost every raid target is a quake target, and a column of fifty
	// `true`s is a column nobody reads — the exception is what deserves the reader's attention.
	QuakeImmune bool
}

// Embedded returns the raid targets this binary knows about.
//
// **What is here is what we are confident about.** The set is deliberately not padded: a target
// whose zone or category we would have to guess at is a target we would be publishing a confident
// mistake about, and `createRaidTarget` exists so an operator can add what their server has and we
// did not. `make status` does not track this list, because "complete" is not a property it can
// have — P99 adds and moves things.
//
// The order is expansion, then zone, then name, and it is load-bearing rather than tidy: ids are
// minted in this order and the collection pages on the ULID cursor, so this order IS the order a
// client reads the catalogue in.
//
// The `key_holder` category is in the enum and is used by nothing here. That is honest rather than
// an oversight: it belongs to the keying chains, we are not confident which mobs P99 currently
// treats as holding which key, and guessing would put a wrong category on the board.
func Embedded() []EmbeddedTarget {
	return []EmbeddedTarget{
		// --- classic -------------------------------------------------------------------------
		{
			Name: "Phinigel Autropos", Zone: "Kedge Keep",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"Phinny", "Phin"},
		},
		{
			Name: "Lord Nagafen", Zone: "Nagafen's Lair",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"Naggy", "Nagafen", "Nag"},
		},
		{
			Name: "Lady Vox", Zone: "Permafrost Caverns",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"Vox"},
		},
		{
			Name: "Cazic-Thule", Zone: "Plane of Fear",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryPlanar,
			Aliases:   []string{"CT", "Cazic"},
		},
		{
			Name: "Dread", Zone: "Plane of Fear",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryPlanar,
		},
		{
			Name: "Fright", Zone: "Plane of Fear",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryPlanar,
		},
		{
			Name: "Terror", Zone: "Plane of Fear",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryPlanar,
		},
		{
			Name: "Innoruuk", Zone: "Plane of Hate",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryPlanar,
			Aliases:   []string{"Inny"},
		},
		{
			Name: "Maestro of Rancor", Zone: "Plane of Hate",
			Expansion: schemaenum.RaidTargetExpansionClassic,
			Category:  schemaenum.RaidTargetCategoryPlanar,
			Aliases:   []string{"Maestro"},
		},

		// --- kunark --------------------------------------------------------------------------
		{
			Name: "Gorenaire", Zone: "The Dreadlands",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
			Aliases:   []string{"Gore"},
		},
		{
			Name: "Severilous", Zone: "The Emerald Jungle",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
			Aliases:   []string{"Sev"},
		},
		{
			Name: "Venril Sathir", Zone: "Karnor's Castle",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"VS"},
		},
		{
			Name: "Trakanon", Zone: "Old Sebilis",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"Trak"},
		},
		{
			Name: "Talendor", Zone: "Skyfire Mountains",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
			Aliases:   []string{"Tal"},
		},
		{
			Name: "Faydedar", Zone: "Timorous Deep",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
			Aliases:   []string{"Fay"},
		},
		{
			Name: "Druushk", Zone: "Veeshan's Peak",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
		},
		{
			Name: "Hoshkar", Zone: "Veeshan's Peak",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
		},
		{
			Name: "Nexona", Zone: "Veeshan's Peak",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
		},
		{
			Name: "Phara Dar", Zone: "Veeshan's Peak",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
		},
		{
			Name: "Silverwing", Zone: "Veeshan's Peak",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
		},
		{
			Name: "Xygoz", Zone: "Veeshan's Peak",
			Expansion: schemaenum.RaidTargetExpansionKunark,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
		},

		// --- velious -------------------------------------------------------------------------
		{
			Name: "Kelorek`Dar", Zone: "Cobalt Scar",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
			Aliases:   []string{"Kelorek"},
		},
		{
			Name: "Zlandicar", Zone: "Dragon Necropolis",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
		},
		{
			Name: "Avatar of War", Zone: "Kael Drakkel",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"AoW"},
		},
		{
			Name: "King Tormax", Zone: "Kael Drakkel",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"Tormax"},
		},
		{
			Name: "Statue of Rallos Zek", Zone: "Kael Drakkel",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
			Aliases:   []string{"Statue", "SoRZ"},
		},
		{
			Name: "Tunare", Zone: "Plane of Growth",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryPlanar,
		},
		{
			Name: "Yelinak", Zone: "Skyshrine",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryZoneBoss,
		},
		{
			Name: "Hraashna the Warder", Zone: "Sleeper's Tomb",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategorySleeper,
			Aliases:   []string{"Hraashna"},
		},
		{
			Name: "Kerafyrm", Zone: "Sleeper's Tomb",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategorySleeper,
			Aliases:   []string{"The Sleeper", "Sleeper"},
			// The one target here a quake does not repop, and the only one we are confident enough
			// about to say so: waking Kerafyrm is a one-time server event, not a spawn cycle. Every
			// other flag on this list is the schema's default rather than a claim.
			QuakeImmune: true,
		},
		{
			Name: "Nanzata the Warder", Zone: "Sleeper's Tomb",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategorySleeper,
			Aliases:   []string{"Nanzata"},
		},
		{
			Name: "Tukaarak the Warder", Zone: "Sleeper's Tomb",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategorySleeper,
			Aliases:   []string{"Tukaarak"},
		},
		{
			Name: "Ventani the Warder", Zone: "Sleeper's Tomb",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategorySleeper,
			Aliases:   []string{"Ventani"},
		},
		{
			Name: "Aaryonar", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
		},
		{
			Name: "Cekenar", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
		},
		{
			Name: "Dagarn the Destroyer", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Dagarn"},
		},
		{
			Name: "Eashen of the Sky", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Eashen"},
		},
		{
			Name: "Gozzrem", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
		},
		{
			Name: "Ikatiar the Venom", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Ikatiar"},
		},
		{
			Name: "Jorlleag", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
		},
		{
			Name: "Lady Mirenilla", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Mirenilla"},
		},
		{
			Name: "Lady Nevederia", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Nevederia"},
		},
		{
			Name: "Lendiniara the Keeper", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Lendiniara"},
		},
		{
			Name: "Lord Feshlak", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Feshlak"},
		},
		{
			Name: "Lord Koi`Doken", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Koi`Doken"},
		},
		{
			Name: "Lord Kreizenn", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Kreizenn"},
		},
		{
			Name: "Lord Vyemm", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Vyemm"},
		},
		{
			Name: "Sevalak", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
		},
		{
			Name: "Telkorenar", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
		},
		{
			// The backtick is canonical and is the whole reason `name_norm` strips one: an officer
			// types this as Vulak'Aerr, VulakAerr or vulak aerr, and all four have to land here.
			Name: "Vulak`Aerr", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
			Aliases:   []string{"Vulak", "VA"},
		},
		{
			Name: "Zlexak", Zone: "Temple of Veeshan",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryNToV,
		},
		{
			Name: "Wuoshi", Zone: "The Wakening Land",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
		},
		{
			Name: "Klandicar", Zone: "Western Wastes",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
		},
		{
			Name: "Sontalak", Zone: "Western Wastes",
			Expansion: schemaenum.RaidTargetExpansionVelious,
			Category:  schemaenum.RaidTargetCategoryOpenWorld,
		},
	}
}

// TargetSeedReport is what a run of [Service.SeedTargets] did.
//
// Every number is reported, including the ones that mean "nothing happened", because a seed that
// prints only "ok" is one an operator cannot tell apart from a seed that silently did nothing —
// and re-running it is exactly what they will do when they think it did.
type TargetSeedReport struct {
	// TargetsAdded were not in the catalogue and now are.
	TargetsAdded int
	// TargetsPresent were already there, matched on `name_norm`, and were left ALONE — including
	// their zone, category and state. An operator who corrected a row or retired a target must be
	// able to re-run the seed without losing that, or they will stop re-running it.
	TargetsPresent int
	// AliasesAdded were missing from a target that already existed. Aliases are topped up rather
	// than skipped with the target, so a spelling added in a later release reaches an instance
	// that was seeded before it. The cost is that an alias somebody deleted comes back.
	AliasesAdded int
	// AliasesTaken were skipped because that normalised spelling already resolves to a DIFFERENT
	// target. It is counted rather than silently dropped: it means our list and this instance
	// disagree about what a short name means, and the instance wins.
	AliasesTaken int
}

// SeedTargets writes the embedded identity into the catalogue.
//
// It is ADDITIVE and never destructive: an existing target is left exactly as it is. That is the
// property that makes it safe to run on every upgrade, which is the only way a seed stays useful
// after the first install.
//
// The whole run is one transaction. A seed that half-applied would leave a catalogue in a state
// nobody chose and no re-run could distinguish from a complete one.
func (s *Service) SeedTargets(ctx context.Context) (TargetSeedReport, error) {
	var report TargetSeedReport
	now := s.clock.Now()

	err := s.db.InTx(ctx, func(ctx context.Context, q *sqlitegen.Queries) error {
		existing, txErr := q.ListAllRaidTargets(ctx)
		if txErr != nil {
			return txErr
		}
		byName := make(map[string]sqlitegen.RaidTarget, len(existing))
		for _, row := range existing {
			byName[row.NameNorm] = row
		}
		aliasRows, txErr := q.ListAllRaidTargetAliases(ctx)
		if txErr != nil {
			return txErr
		}
		aliasOwner := make(map[string]string, len(aliasRows))
		for _, row := range aliasRows {
			aliasOwner[row.AliasNorm] = row.TargetID
		}

		for _, want := range Embedded() {
			fields, vErr := validateIdentity(want.Name, want.Zone, want.Expansion, want.Category)
			if vErr != nil {
				// The embedded list is our own literals, so this is a bug in this file rather than
				// in an operator's input. It is checked anyway, and refused rather than skipped:
				// TestEmbedded_EveryTarget_IsValid is the gate, and this is what happens if
				// somebody adds a row without running it.
				return vErr
			}
			targetID := ""
			if row, ok := byName[fields.nameNorm]; ok {
				report.TargetsPresent++
				targetID = row.ID
			} else {
				id, idErr := core.NewID[core.RaidTarget](s.ids, now)
				if idErr != nil {
					return idErr
				}
				if _, txErr = q.CreateRaidTarget(ctx, sqlitegen.CreateRaidTargetParams{
					ID: id.String(), Name: fields.name, NameNorm: fields.nameNorm,
					Zone: fields.zone, ZoneNorm: fields.zoneNorm,
					Expansion: fields.expansion, Category: fields.category,
					IsQuakeTarget: boolToInt(!want.QuakeImmune),
					State:         schemaenum.RaidTargetStateActive,
					CreatedAt:     int64(now), UpdatedAt: int64(now),
				}); txErr != nil {
					return txErr
				}
				report.TargetsAdded++
				targetID = id.String()
			}

			for _, alias := range want.Aliases {
				norm := core.Normalise(alias)
				if owner, taken := aliasOwner[norm]; taken {
					if owner != targetID {
						report.AliasesTaken++
					}
					continue
				}
				aliasID, idErr := core.NewID[core.RaidTargetAlias](s.ids, now)
				if idErr != nil {
					return idErr
				}
				if _, txErr = q.CreateRaidTargetAlias(ctx, sqlitegen.CreateRaidTargetAliasParams{
					ID: aliasID.String(), TargetID: targetID,
					Alias: alias, AliasNorm: norm,
					CreatedAt: int64(now), UpdatedAt: int64(now),
				}); txErr != nil {
					return txErr
				}
				aliasOwner[norm] = targetID
				report.AliasesAdded++
			}
		}
		return nil
	})
	if err != nil {
		if coded, ok := apierr.From(err); ok {
			return TargetSeedReport{}, coded
		}
		if store.IsUniqueViolation(err) {
			return TargetSeedReport{}, apierr.Wrap(apierr.CodeConflict, err,
				"the embedded catalogue collides with a target already on this instance")
		}
		return TargetSeedReport{}, apierr.Wrap(apierr.CodeInternalError, err, "")
	}

	s.log.InfoContext(ctx, "raid target identity seeded",
		slog.Int("targets_added", report.TargetsAdded),
		slog.Int("targets_present", report.TargetsPresent),
		slog.Int("aliases_added", report.AliasesAdded),
		slog.Int("aliases_taken", report.AliasesTaken))
	return report, nil
}
