-- +goose Up
-- create "tod_meta" table
CREATE TABLE tod_meta (key text NOT NULL, value text NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (key)) WITHOUT ROWID, STRICT;
-- create "instance" table
CREATE TABLE instance (id integer NOT NULL, name text NOT NULL, public_url text NOT NULL, timezone text NOT NULL DEFAULT 'UTC', self_service_circle_creation integer NOT NULL DEFAULT 0, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT ck_instance_singleton CHECK (id = 1), CONSTRAINT ck_instance_self_service_circle_creation CHECK (self_service_circle_creation IN (0, 1))) STRICT;
-- create "identity_provider" table
CREATE TABLE identity_provider (id text NOT NULL, key text NOT NULL, kind text NOT NULL, display_name text NOT NULL, enabled integer NOT NULL DEFAULT 0, verifiable_subject integer NOT NULL, issuer text, authorization_endpoint text, jwks_uri text, subject_claim text, client_id text, client_secret text, redirect_uri text, token_endpoint text, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT ck_identity_provider_kind CHECK (kind IN ('discord', 'oidc', 'local')), CONSTRAINT ck_identity_provider_enabled CHECK (enabled IN (0, 1)), CONSTRAINT ck_identity_provider_verifiable_subject CHECK (verifiable_subject IN (0, 1)), CONSTRAINT ck_identity_provider_local_is_unverifiable CHECK ((kind = 'local') = (verifiable_subject = 0)), CONSTRAINT ck_identity_provider_discord_has_application CHECK ((kind = 'discord') = (client_id IS NOT NULL))) STRICT;
-- create index "ux_identity_provider_key" to table: "identity_provider"
CREATE UNIQUE INDEX ux_identity_provider_key ON identity_provider (key);
-- create index "ux_identity_provider_discord" to table: "identity_provider"
CREATE UNIQUE INDEX ux_identity_provider_discord ON identity_provider (kind) WHERE kind = 'discord';
-- create index "ux_identity_provider_local" to table: "identity_provider"
CREATE UNIQUE INDEX ux_identity_provider_local ON identity_provider (kind) WHERE kind = 'local';
-- create "identity" table
CREATE TABLE identity (id text NOT NULL, provider_id text NOT NULL, subject text NOT NULL, display_name text NOT NULL DEFAULT '', blocked_at integer, blocked_by_membership_id text, block_reason text, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_identity_provider FOREIGN KEY (provider_id) REFERENCES identity_provider (id), CONSTRAINT fk_identity_blocked_by FOREIGN KEY (blocked_by_membership_id) REFERENCES membership (id), CONSTRAINT ck_identity_block_is_attributed CHECK ((blocked_at IS NULL) = (blocked_by_membership_id IS NULL))) STRICT;
-- create index "ux_identity_provider_subject" to table: "identity"
CREATE UNIQUE INDEX ux_identity_provider_subject ON identity (provider_id, subject);
-- create "identity_link" table
CREATE TABLE identity_link (id text NOT NULL, primary_identity_id text NOT NULL, linked_identity_id text NOT NULL, method text NOT NULL, linked_by_membership_id text NOT NULL, linked_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_identity_link_primary FOREIGN KEY (primary_identity_id) REFERENCES identity (id), CONSTRAINT fk_identity_link_linked FOREIGN KEY (linked_identity_id) REFERENCES identity (id), CONSTRAINT fk_identity_link_linked_by FOREIGN KEY (linked_by_membership_id) REFERENCES membership (id), CONSTRAINT ck_identity_link_distinct CHECK (primary_identity_id <> linked_identity_id), CONSTRAINT ck_identity_link_method CHECK (method IN ('officer_asserted', 'provider_verified'))) STRICT;
-- create index "ux_identity_link_pair" to table: "identity_link"
CREATE UNIQUE INDEX ux_identity_link_pair ON identity_link (primary_identity_id, linked_identity_id);
-- create index "ix_identity_link_linked" to table: "identity_link"
CREATE INDEX ix_identity_link_linked ON identity_link (linked_identity_id);
-- create "auth_flow" table
CREATE TABLE auth_flow (id text NOT NULL, state text NOT NULL, pkce_verifier text NOT NULL, provider_id text NOT NULL, invite_code_hash blob, circle_id text, expires_at integer NOT NULL, consumed_at integer, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_auth_flow_provider FOREIGN KEY (provider_id) REFERENCES identity_provider (id), CONSTRAINT fk_auth_flow_circle FOREIGN KEY (circle_id) REFERENCES circle (id)) STRICT;
-- create index "ux_auth_flow_state" to table: "auth_flow"
CREATE UNIQUE INDEX ux_auth_flow_state ON auth_flow (state);
-- create index "ix_auth_flow_expires_at" to table: "auth_flow"
CREATE INDEX ix_auth_flow_expires_at ON auth_flow (expires_at);
-- create "credential_ticket" table
CREATE TABLE credential_ticket (id text NOT NULL, ticket_hash blob NOT NULL, provider_id text NOT NULL, subject text NOT NULL, display_name text NOT NULL DEFAULT '', guild_roles_json text NOT NULL DEFAULT '{}', expires_at integer NOT NULL, consumed_at integer, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_credential_ticket_provider FOREIGN KEY (provider_id) REFERENCES identity_provider (id), CONSTRAINT ck_credential_ticket_ttl CHECK (expires_at = created_at + 120 * 1000000), CONSTRAINT ck_credential_ticket_guild_roles_json CHECK (json_valid(guild_roles_json))) STRICT;
-- create index "ux_credential_ticket_hash" to table: "credential_ticket"
CREATE UNIQUE INDEX ux_credential_ticket_hash ON credential_ticket (ticket_hash);
-- create index "ix_credential_ticket_expires_at" to table: "credential_ticket"
CREATE INDEX ix_credential_ticket_expires_at ON credential_ticket (expires_at);
-- create "raid_target" table
CREATE TABLE raid_target (id text NOT NULL, name text NOT NULL, name_norm text NOT NULL, zone text NOT NULL, zone_norm text NOT NULL, expansion text NOT NULL, category text NOT NULL, is_quake_target integer NOT NULL DEFAULT 1, state text NOT NULL DEFAULT 'active', created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT ck_raid_target_expansion CHECK (expansion IN ('classic', 'kunark', 'velious')), CONSTRAINT ck_raid_target_category CHECK (category IN ('open_world', 'zone_boss', 'planar', 'ntov', 'sleeper', 'key_holder')), CONSTRAINT ck_raid_target_state CHECK (state IN ('active', 'retired')), CONSTRAINT ck_raid_target_is_quake_target CHECK (is_quake_target IN (0, 1))) STRICT;
-- create index "ux_raid_target_name_norm" to table: "raid_target"
CREATE UNIQUE INDEX ux_raid_target_name_norm ON raid_target (name_norm);
-- create index "ix_raid_target_zone_norm" to table: "raid_target"
CREATE INDEX ix_raid_target_zone_norm ON raid_target (zone_norm);
-- create "raid_target_alias" table
CREATE TABLE raid_target_alias (id text NOT NULL, target_id text NOT NULL, alias text NOT NULL, alias_norm text NOT NULL, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_raid_target_alias_target FOREIGN KEY (target_id) REFERENCES raid_target (id)) STRICT;
-- create index "ux_raid_target_alias_norm" to table: "raid_target_alias"
CREATE UNIQUE INDEX ux_raid_target_alias_norm ON raid_target_alias (alias_norm);
-- create index "ix_raid_target_alias_target" to table: "raid_target_alias"
CREATE INDEX ix_raid_target_alias_target ON raid_target_alias (target_id);
-- create "raid_target_timer" table
CREATE TABLE raid_target_timer (target_id text NOT NULL, server text NOT NULL, window_kind text NOT NULL, window_open_offset_seconds integer, window_close_offset_seconds integer, fixed_grace_seconds integer NOT NULL DEFAULT 900, cluster_epsilon_seconds integer, source text, note text NOT NULL DEFAULT '', created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (target_id, server), CONSTRAINT fk_raid_target_timer_target FOREIGN KEY (target_id) REFERENCES raid_target (id), CONSTRAINT ck_raid_target_timer_server CHECK (server IN ('blue', 'green', 'red')), CONSTRAINT ck_raid_target_timer_window_kind CHECK (window_kind IN ('fixed', 'variance', 'unknown')), CONSTRAINT ck_raid_target_timer_unknown_has_no_offsets CHECK ((window_kind = 'unknown') = (window_open_offset_seconds IS NULL)), CONSTRAINT ck_raid_target_timer_offsets_are_paired CHECK ((window_open_offset_seconds IS NULL) = (window_close_offset_seconds IS NULL)), CONSTRAINT ck_raid_target_timer_fixed_is_a_point CHECK ((window_kind = 'fixed') = (window_open_offset_seconds IS NOT NULL AND window_close_offset_seconds IS NOT NULL AND window_open_offset_seconds = window_close_offset_seconds)), CONSTRAINT ck_raid_target_timer_window_is_ordered CHECK (window_open_offset_seconds IS NULL OR window_close_offset_seconds IS NULL OR window_close_offset_seconds >= window_open_offset_seconds), CONSTRAINT ck_raid_target_timer_fixed_grace_seconds CHECK (fixed_grace_seconds >= 0)) STRICT;
-- create "api_token" table
CREATE TABLE api_token (id text NOT NULL, membership_id text NOT NULL, token_prefix text NOT NULL, token_hash blob NOT NULL, name text NOT NULL DEFAULT '', scopes_json text NOT NULL DEFAULT '[]', last_used_at integer, expires_at integer, revoked_at integer, revoked_by_membership_id text, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_api_token_membership FOREIGN KEY (membership_id) REFERENCES membership (id), CONSTRAINT fk_api_token_revoked_by FOREIGN KEY (revoked_by_membership_id) REFERENCES membership (id), CONSTRAINT ck_api_token_prefix_length CHECK (length(token_prefix) = 8), CONSTRAINT ck_api_token_scopes_json CHECK (json_valid(scopes_json))) STRICT;
-- create index "ux_api_token_hash" to table: "api_token"
CREATE UNIQUE INDEX ux_api_token_hash ON api_token (token_hash);
-- create index "ix_api_token_membership" to table: "api_token"
CREATE INDEX ix_api_token_membership ON api_token (membership_id);
-- create index "ix_api_token_prefix" to table: "api_token"
CREATE INDEX ix_api_token_prefix ON api_token (token_prefix);
-- create "idempotency_record" table
CREATE TABLE idempotency_record (id text NOT NULL, principal_membership_id text NOT NULL, key text NOT NULL, request_hash blob NOT NULL, response_status integer, response_body text, completed_at integer, expires_at integer NOT NULL, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_idempotency_record_principal FOREIGN KEY (principal_membership_id) REFERENCES membership (id), CONSTRAINT ck_idempotency_record_completed_has_a_response CHECK ((completed_at IS NULL) = (response_status IS NULL))) STRICT;
-- create index "ux_idempotency_record_principal_key" to table: "idempotency_record"
CREATE UNIQUE INDEX ux_idempotency_record_principal_key ON idempotency_record (principal_membership_id, key);
-- create index "ix_idempotency_record_expires_at" to table: "idempotency_record"
CREATE INDEX ix_idempotency_record_expires_at ON idempotency_record (expires_at);
-- create "event_outbox" table
CREATE TABLE event_outbox (event_seq integer NOT NULL PRIMARY KEY AUTOINCREMENT, id text NOT NULL, circle_id text, kind text NOT NULL, payload_json text NOT NULL DEFAULT '{}', created_at integer NOT NULL, CONSTRAINT fk_event_outbox_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT ck_event_outbox_payload_json CHECK (json_valid(payload_json))) STRICT;
-- create index "ux_event_outbox_id" to table: "event_outbox"
CREATE UNIQUE INDEX ux_event_outbox_id ON event_outbox (id);
-- create index "ix_event_outbox_circle_seq" to table: "event_outbox"
CREATE INDEX ix_event_outbox_circle_seq ON event_outbox (circle_id, event_seq);
-- create "circle" table
CREATE TABLE circle (id text NOT NULL, name text NOT NULL, name_norm text NOT NULL, description text NOT NULL DEFAULT '', server text NOT NULL, timezone text NOT NULL DEFAULT 'UTC', min_reporters_to_supersede integer NOT NULL DEFAULT 1, revoke_invalidates_invites integer NOT NULL DEFAULT 1, state text NOT NULL DEFAULT 'active', created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT ck_circle_server CHECK (server IN ('blue', 'green', 'red')), CONSTRAINT ck_circle_state CHECK (state IN ('active', 'archived')), CONSTRAINT ck_circle_min_reporters_to_supersede CHECK (min_reporters_to_supersede >= 1), CONSTRAINT ck_circle_revoke_invalidates_invites CHECK (revoke_invalidates_invites IN (0, 1))) STRICT;
-- create index "ux_circle_name_norm_server" to table: "circle"
CREATE UNIQUE INDEX ux_circle_name_norm_server ON circle (name_norm, server);
-- create "circle_provider" table
CREATE TABLE circle_provider (circle_id text NOT NULL, provider_id text NOT NULL, discord_guild_id text, discord_required_role_ids_json text NOT NULL DEFAULT '[]', created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (circle_id, provider_id), CONSTRAINT fk_circle_provider_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_circle_provider_provider FOREIGN KEY (provider_id) REFERENCES identity_provider (id), CONSTRAINT ck_circle_provider_required_role_ids_json CHECK (json_valid(discord_required_role_ids_json))) STRICT;
-- create "membership" table
CREATE TABLE membership (id text NOT NULL, circle_id text NOT NULL, identity_id text, kind text NOT NULL, owner_membership_id text, display_name text NOT NULL, display_name_norm text NOT NULL, role text NOT NULL, admitted_by_invite_id text, joined_at integer NOT NULL, revoked_at integer, revoked_by_membership_id text, revoke_reason text, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_membership_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_membership_identity FOREIGN KEY (identity_id) REFERENCES identity (id), CONSTRAINT fk_membership_owner FOREIGN KEY (circle_id, owner_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT fk_membership_invite FOREIGN KEY (circle_id, admitted_by_invite_id) REFERENCES invite (circle_id, id), CONSTRAINT fk_membership_revoked_by FOREIGN KEY (circle_id, revoked_by_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT ck_membership_kind CHECK (kind IN ('human', 'service')), CONSTRAINT ck_membership_role CHECK (role IN ('owner', 'officer', 'member', 'observer')), CONSTRAINT ck_membership_human_has_an_identity CHECK ((kind = 'human') = (identity_id IS NOT NULL)), CONSTRAINT ck_membership_service_has_an_owner CHECK ((kind = 'service') = (owner_membership_id IS NOT NULL)), CONSTRAINT ck_membership_revocation_is_attributed CHECK ((revoked_at IS NULL) = (revoked_by_membership_id IS NULL))) STRICT;
-- create index "ux_membership_circle_id" to table: "membership"
CREATE UNIQUE INDEX ux_membership_circle_id ON membership (circle_id, id);
-- create index "ux_membership_identity" to table: "membership"
CREATE UNIQUE INDEX ux_membership_identity ON membership (circle_id, identity_id) WHERE identity_id IS NOT NULL;
-- create index "ix_membership_identity_id" to table: "membership"
CREATE INDEX ix_membership_identity_id ON membership (identity_id);
-- create index "ix_membership_circle_display_name_norm" to table: "membership"
CREATE INDEX ix_membership_circle_display_name_norm ON membership (circle_id, display_name_norm);
-- create "invite" table
CREATE TABLE invite (id text NOT NULL, circle_id text NOT NULL, code_hash blob NOT NULL, code_prefix text NOT NULL, role text NOT NULL, max_uses integer NOT NULL, uses integer NOT NULL DEFAULT 0, expires_at integer NOT NULL, revoked_at integer, created_by_membership_id text NOT NULL, minted_by_kind text NOT NULL, note text NOT NULL DEFAULT '', created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_invite_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_invite_created_by FOREIGN KEY (circle_id, created_by_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT ck_invite_role CHECK (role IN ('owner', 'officer', 'member', 'observer')), CONSTRAINT ck_invite_role_is_not_owner CHECK (role <> 'owner'), CONSTRAINT ck_invite_minted_by_kind CHECK (minted_by_kind IN ('session', 'pat')), CONSTRAINT ck_invite_max_uses CHECK (max_uses >= 1), CONSTRAINT ck_invite_uses_within_max CHECK (uses >= 0 AND uses <= max_uses)) STRICT;
-- create index "ux_invite_code_hash" to table: "invite"
CREATE UNIQUE INDEX ux_invite_code_hash ON invite (code_hash);
-- create index "ux_invite_circle_id" to table: "invite"
CREATE UNIQUE INDEX ux_invite_circle_id ON invite (circle_id, id);
-- create "invite_redemption" table
CREATE TABLE invite_redemption (id text NOT NULL, circle_id text NOT NULL, invite_id text NOT NULL, membership_id text NOT NULL, identity_id text, created_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_invite_redemption_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_invite_redemption_invite FOREIGN KEY (circle_id, invite_id) REFERENCES invite (circle_id, id), CONSTRAINT fk_invite_redemption_membership FOREIGN KEY (circle_id, membership_id) REFERENCES membership (circle_id, id), CONSTRAINT fk_invite_redemption_identity FOREIGN KEY (identity_id) REFERENCES identity (id)) STRICT;
-- create index "ix_invite_redemption_invite" to table: "invite_redemption"
CREATE INDEX ix_invite_redemption_invite ON invite_redemption (invite_id);
-- create index "ix_invite_redemption_circle_created" to table: "invite_redemption"
CREATE INDEX ix_invite_redemption_circle_created ON invite_redemption (circle_id, created_at);
-- create "tod_report" table
CREATE TABLE tod_report (id text NOT NULL, circle_id text NOT NULL, target_id text NOT NULL, kind text NOT NULL, died_at integer NOT NULL, reported_at integer NOT NULL, reporter_membership_id text NOT NULL, source text NOT NULL, self_confidence text NOT NULL, source_line text, source_character text, log_character text, killed_by_guild text, client_clock_offset_seconds integer, retracts_report_id text, PRIMARY KEY (id), CONSTRAINT fk_tod_report_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_tod_report_target FOREIGN KEY (target_id) REFERENCES raid_target (id), CONSTRAINT fk_tod_report_reporter FOREIGN KEY (circle_id, reporter_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT fk_tod_report_retracts FOREIGN KEY (circle_id, retracts_report_id) REFERENCES tod_report (circle_id, id), CONSTRAINT ck_tod_report_kind CHECK (kind IN ('kill', 'retraction')), CONSTRAINT ck_tod_report_source CHECK (source IN ('log_line', 'manual', 'api', 'import')), CONSTRAINT ck_tod_report_self_confidence CHECK (self_confidence IN ('certain', 'probable', 'guess')), CONSTRAINT ck_tod_report_retraction_names_a_report CHECK ((kind = 'retraction') = (retracts_report_id IS NOT NULL)), CONSTRAINT ck_tod_report_died_at_not_in_future CHECK (died_at <= reported_at + 120 * 1000000)) STRICT;
-- create index "ux_tod_report_circle_id" to table: "tod_report"
CREATE UNIQUE INDEX ux_tod_report_circle_id ON tod_report (circle_id, id);
-- create index "ux_tod_report_natural" to table: "tod_report"
CREATE UNIQUE INDEX ux_tod_report_natural ON tod_report (circle_id, target_id, reporter_membership_id, died_at) WHERE kind = 'kill';
-- create index "ux_tod_report_retracts" to table: "tod_report"
CREATE UNIQUE INDEX ux_tod_report_retracts ON tod_report (retracts_report_id) WHERE retracts_report_id IS NOT NULL;
-- create index "ix_tod_report_circle_target_died" to table: "tod_report"
CREATE INDEX ix_tod_report_circle_target_died ON tod_report (circle_id, target_id, died_at);
-- create index "ix_tod_report_circle_reporter" to table: "tod_report"
CREATE INDEX ix_tod_report_circle_reporter ON tod_report (circle_id, reporter_membership_id);
-- create "quake_event" table
CREATE TABLE quake_event (id text NOT NULL, circle_id text NOT NULL, occurred_at integer NOT NULL, reported_at integer NOT NULL, reported_by_membership_id text NOT NULL, source text NOT NULL, note text NOT NULL DEFAULT '', PRIMARY KEY (id), CONSTRAINT fk_quake_event_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_quake_event_reporter FOREIGN KEY (circle_id, reported_by_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT ck_quake_event_source CHECK (source IN ('log_line', 'manual', 'api', 'import')), CONSTRAINT ck_quake_event_occurred_at_not_in_future CHECK (occurred_at <= reported_at + 120 * 1000000)) STRICT;
-- create index "ix_quake_event_circle_occurred" to table: "quake_event"
CREATE INDEX ix_quake_event_circle_occurred ON quake_event (circle_id, occurred_at);
-- create "circle_timer_override" table
CREATE TABLE circle_timer_override (circle_id text NOT NULL, target_id text NOT NULL, window_kind text NOT NULL, window_open_offset_seconds integer, window_close_offset_seconds integer, fixed_grace_seconds integer NOT NULL DEFAULT 900, cluster_epsilon_seconds integer, note text NOT NULL DEFAULT '', created_by_membership_id text NOT NULL, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (circle_id, target_id), CONSTRAINT fk_circle_timer_override_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_circle_timer_override_target FOREIGN KEY (target_id) REFERENCES raid_target (id), CONSTRAINT fk_circle_timer_override_created_by FOREIGN KEY (circle_id, created_by_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT ck_circle_timer_override_window_kind CHECK (window_kind IN ('fixed', 'variance', 'unknown')), CONSTRAINT ck_circle_timer_override_unknown_has_no_offsets CHECK ((window_kind = 'unknown') = (window_open_offset_seconds IS NULL)), CONSTRAINT ck_circle_timer_override_offsets_are_paired CHECK ((window_open_offset_seconds IS NULL) = (window_close_offset_seconds IS NULL)), CONSTRAINT ck_circle_timer_override_fixed_is_a_point CHECK ((window_kind = 'fixed') = (window_open_offset_seconds IS NOT NULL AND window_close_offset_seconds IS NOT NULL AND window_open_offset_seconds = window_close_offset_seconds)), CONSTRAINT ck_circle_timer_override_window_is_ordered CHECK (window_open_offset_seconds IS NULL OR window_close_offset_seconds IS NULL OR window_close_offset_seconds >= window_open_offset_seconds), CONSTRAINT ck_circle_timer_override_fixed_grace_seconds CHECK (fixed_grace_seconds >= 0)) STRICT;
-- create "target_state_cache" table
CREATE TABLE target_state_cache (circle_id text NOT NULL, target_id text NOT NULL, computed_at integer NOT NULL, latest_report_id text, report_count integer NOT NULL DEFAULT 0, status text NOT NULL, confidence text NOT NULL, contested integer NOT NULL DEFAULT 0, contest_reason text, change_reason text, died_at integer, window_open_at integer, window_close_at integer, spawn_at integer, distinct_reporter_count integer NOT NULL DEFAULT 0, log_line_count integer NOT NULL DEFAULT 0, spread_seconds integer, revoked_reporter_count integer NOT NULL DEFAULT 0, created_at integer NOT NULL, updated_at integer NOT NULL, PRIMARY KEY (circle_id, target_id), CONSTRAINT fk_target_state_cache_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_target_state_cache_target FOREIGN KEY (target_id) REFERENCES raid_target (id), CONSTRAINT fk_target_state_cache_latest_report FOREIGN KEY (circle_id, latest_report_id) REFERENCES tod_report (circle_id, id), CONSTRAINT ck_target_state_cache_status CHECK (status IN ('unknown', 'no_timer', 'pre_window', 'in_window', 'overdue', 'up')), CONSTRAINT ck_target_state_cache_confidence CHECK (confidence IN ('unknown', 'low', 'medium', 'high')), CONSTRAINT ck_target_state_cache_contest_reason CHECK (contest_reason IN ('thin_supersede', 'implausible_ordering', 'wide_spread', 'pending_supersede')), CONSTRAINT ck_target_state_cache_change_reason CHECK (change_reason IN ('new_kill', 'corroboration', 'retraction', 'quake', 'timer_change')), CONSTRAINT ck_target_state_cache_contested CHECK (contested IN (0, 1)), CONSTRAINT ck_target_state_cache_contested_has_a_reason CHECK ((contested = 1) = (contest_reason IS NOT NULL))) STRICT;
-- create index "ix_target_state_cache_circle_status" to table: "target_state_cache"
CREATE INDEX ix_target_state_cache_circle_status ON target_state_cache (circle_id, status);
-- create index "ix_target_state_cache_circle_window_open" to table: "target_state_cache"
CREATE INDEX ix_target_state_cache_circle_window_open ON target_state_cache (circle_id, window_open_at);
-- create "audit_log" table
CREATE TABLE audit_log (id text NOT NULL, circle_id text NOT NULL, actor_membership_id text, action text NOT NULL, entity_type text NOT NULL, entity_id text, detail_json text NOT NULL DEFAULT '{}', prev_hash blob, hash blob NOT NULL, created_at integer NOT NULL, PRIMARY KEY (id), CONSTRAINT fk_audit_log_circle FOREIGN KEY (circle_id) REFERENCES circle (id), CONSTRAINT fk_audit_log_actor FOREIGN KEY (circle_id, actor_membership_id) REFERENCES membership (circle_id, id), CONSTRAINT ck_audit_log_detail_json CHECK (json_valid(detail_json))) STRICT;
-- create index "ux_audit_log_hash" to table: "audit_log"
CREATE UNIQUE INDEX ux_audit_log_hash ON audit_log (hash);
-- create index "ix_audit_log_circle_id" to table: "audit_log"
CREATE INDEX ix_audit_log_circle_id ON audit_log (circle_id, id);

-- +goose Down
-- +goose StatementBegin
SELECT RAISE(ABORT, 'migrations are forward-only; roll forward with a new migration');
-- +goose StatementEnd
