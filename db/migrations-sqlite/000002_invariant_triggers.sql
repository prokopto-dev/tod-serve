-- The invariants, as triggers. Hand-written, and the ONLY hand-written migration in this
-- directory: Atlas Community neither inspects nor emits SQLite triggers, so it can neither author
-- these nor plan a diff that drops one. That cuts both ways, and the cost is named here rather
-- than discovered later:
--
--   A table rebuild drops every trigger on the table, silently. SQLite rebuilds a table for any
--   ALTER it cannot do in place, so a future migration can undo this whole file without saying so.
--   TestAppendOnly_TriggersFire_AfterAllMigrations runs AFTER every migration has applied and
--   asserts that each trigger ABORTS a write -- not that it appears in sqlite_master, because a
--   trigger that exists and does not fire is exactly the failure this is guarding against.
--
-- This file adds no tables, columns or indexes. Anything Atlas can see belongs in db/schema.hcl,
-- or the next `make gen` will plan a diff to remove it.

-- +goose Up

-- The report log is append-only: canonical conventions section 10, ADR-0004.
--
-- Corrections are NEW ROWS. A retraction is a row with retracts_report_id set and the original
-- stays visible. This is trigger-enforced rather than reviewed because the whole trust argument
-- for deriving consensus rather than storing it collapses if the evidence can be edited -- and
-- because the tempting fix for a bad report, at 2am, is an UPDATE.

-- +goose StatementBegin
CREATE TRIGGER trg_tod_report_no_update BEFORE UPDATE ON tod_report BEGIN
  SELECT RAISE(ABORT, 'tod_report is append-only: corrections are new rows, retractions are new rows');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_tod_report_no_delete BEFORE DELETE ON tod_report BEGIN
  SELECT RAISE(ABORT, 'tod_report is append-only: reports are never pruned');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_quake_event_no_update BEFORE UPDATE ON quake_event BEGIN
  SELECT RAISE(ABORT, 'quake_event is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_quake_event_no_delete BEFORE DELETE ON quake_event BEGIN
  SELECT RAISE(ABORT, 'quake_event is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_invite_redemption_no_update BEFORE UPDATE ON invite_redemption BEGIN
  SELECT RAISE(ABORT, 'invite_redemption is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_invite_redemption_no_delete BEFORE DELETE ON invite_redemption BEGIN
  SELECT RAISE(ABORT, 'invite_redemption is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_identity_link_no_update BEFORE UPDATE ON identity_link BEGIN
  SELECT RAISE(ABORT, 'identity_link is append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_identity_link_no_delete BEFORE DELETE ON identity_link BEGIN
  SELECT RAISE(ABORT, 'identity_link is append-only: unlinking would reopen the second door');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_audit_log_no_update BEFORE UPDATE ON audit_log BEGIN
  SELECT RAISE(ABORT, 'audit_log is append-only and hash-chained');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_audit_log_no_delete BEFORE DELETE ON audit_log BEGIN
  SELECT RAISE(ABORT, 'audit_log is append-only and hash-chained');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_event_outbox_no_update BEFORE UPDATE ON event_outbox BEGIN
  SELECT RAISE(ABORT, 'event_outbox is append-only: a delivered event is not editable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_event_outbox_no_delete BEFORE DELETE ON event_outbox BEGIN
  SELECT RAISE(ABORT, 'event_outbox is append-only: event_seq must never be reused');
END;
-- +goose StatementEnd

-- A circle is pinned to ONE server, immutably: ADR-0009.
--
-- A CHECK cannot express this, because a CHECK cannot see the old row. The edge answers
-- 422 field_immutable; this is what makes that answer true rather than merely usual. The trigger
-- keys on the value changing rather than on `UPDATE OF server`, so a statement that sets server to
-- something new is refused however it was written.

-- +goose StatementBegin
CREATE TRIGGER trg_circle_server_is_immutable BEFORE UPDATE ON circle
WHEN NEW.server <> OLD.server BEGIN
  SELECT RAISE(ABORT, 'circle.server is immutable: raiding a second server is a second circle');
END;
-- +goose StatementEnd

-- An identity_link participant must have verifiable_subject = 1: 04-identity section 3.
--
-- A `local` identity can never be linked. Silently unifying an unverified identity with a verified
-- one would let anyone who can assert a display name inherit, or resurrect, another person's
-- standing -- and since a link revokes across the whole set, it would do so with the officers
-- believing the opposite.
--
-- The count is compared against 2 rather than checking each side, so a participant whose identity
-- row does not exist at all is refused by the same test.

-- +goose StatementBegin
CREATE TRIGGER trg_identity_link_requires_verifiable_participants
BEFORE INSERT ON identity_link
WHEN (
  SELECT COUNT(*) FROM identity i
  JOIN identity_provider p ON p.id = i.provider_id
  WHERE i.id IN (NEW.primary_identity_id, NEW.linked_identity_id)
    AND p.verifiable_subject = 1
) <> 2 BEGIN
  SELECT RAISE(ABORT, 'identity_link participants must both have verifiable_subject = 1');
END;
-- +goose StatementEnd

-- A credential_ticket is redeemable ONCE: ADR-0011, invariants.
--
-- The TTL is a CHECK in db/schema.hcl, so a ticket that outlives 120 seconds cannot be written.
-- Single use is here, because it is a property of the transition rather than of the row: Go writes
-- `WHERE consumed_at IS NULL` and this is what makes forgetting to survivable. A second redemption
-- mints a second PAT for one authorization, which is the whole thing the ticket exists to prevent.

-- +goose StatementBegin
CREATE TRIGGER trg_credential_ticket_single_use BEFORE UPDATE ON credential_ticket
WHEN OLD.consumed_at IS NOT NULL BEGIN
  SELECT RAISE(ABORT, 'credential_ticket is single-use: it has already been redeemed');
END;
-- +goose StatementEnd

-- The facts a ticket carries are what the provider said at the callback. Nothing may edit them
-- afterwards: the guild roles on this row ARE the guild gate's input, and a gate evaluated against
-- an edited copy is a gate that is not evaluated.

-- +goose StatementBegin
CREATE TRIGGER trg_credential_ticket_facts_are_immutable BEFORE UPDATE ON credential_ticket
WHEN NEW.ticket_hash <> OLD.ticket_hash
  OR NEW.provider_id <> OLD.provider_id
  OR NEW.subject <> OLD.subject
  OR NEW.guild_roles_json <> OLD.guild_roles_json
  OR NEW.expires_at <> OLD.expires_at
  OR NEW.created_at <> OLD.created_at BEGIN
  SELECT RAISE(ABORT, 'credential_ticket facts are immutable after minting; only consumed_at moves');
END;
-- +goose StatementEnd

-- An auth_flow is consumed once, for the same reason. The state is a CSRF nonce and the verifier
-- binds one code exchange; replaying either would let a second exchange ride on the first
-- authorization.

-- +goose StatementBegin
CREATE TRIGGER trg_auth_flow_single_use BEFORE UPDATE ON auth_flow
WHEN OLD.consumed_at IS NOT NULL BEGIN
  SELECT RAISE(ABORT, 'auth_flow is single-use: this state has already been exchanged');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_auth_flow_facts_are_immutable BEFORE UPDATE ON auth_flow
WHEN NEW.state <> OLD.state
  OR NEW.pkce_verifier <> OLD.pkce_verifier
  OR NEW.provider_id <> OLD.provider_id
  OR NEW.expires_at <> OLD.expires_at BEGIN
  SELECT RAISE(ABORT, 'auth_flow facts are immutable after creation; only consumed_at moves');
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
