// Code generated from openapi/openapi.json by web/scripts/generate-client.mjs.
// DO NOT EDIT. Run `make gen-web` from the repository root.
//
// Every request the console makes goes through one of the functions at the bottom of this
// file, and every one of them names an `operationId` the published document carries. There
// is no way to spell a URL here, which is what stops the console growing a capability the
// nParse+ plugin can never reach.

import { send, type CallOptions, type Result } from './http'

/** RFC 3339 with microsecond precision, always UTC. Never read against the browser clock. */
export type Timestamp = string

/** EmptyInput is an operation that takes no path parameter, no query parameter and no body. */
export type EmptyInput = { readonly [key: string]: never }

export type AdminIdentityProvider = {
  authorization_endpoint: string
  client_id: string
  client_secret_set: boolean
  display_name: string
  enabled: boolean
  id: string
  issuer: string
  jwks_uri: string
  key: string
  kind: string
  redirect_uri: string
  subject_claim: string
  token_endpoint: string
  verifiable_subject: boolean
}

export type AdminIdentityProviderResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  authorization_endpoint: string
  client_id: string
  client_secret_set: boolean
  display_name: string
  enabled: boolean
  id: string
  issuer: string
  jwks_uri: string
  key: string
  kind: string
  redirect_uri: string
  subject_claim: string
  token_endpoint: string
  verifiable_subject: boolean
}

export type Alternative = {
  confidence: string
  /** RFC 3339 with microsecond precision, always UTC. */
  died_at: string
  distinct_reporter_count: number
  report_count: number
  report_ids: Array<string> | null
  window: Window
}

export type AuthenticateIdentityInputBody = {
  /** The circle to re-authenticate into */
  circle_id: string
  client?: ClientBody
  credential: CredentialBody
  display_name?: string
  /** A provider key */
  provider: string
  scopes?: Array<string> | null
}

export type AuthorizationStart = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  authorization_url: string
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
}

export type BindCircleDiscordChannelInputBody = {
  /** Permit replies the whole channel can read. Defaults to false, and that default is in the DDL: Discord has no channel-membership API, so nobody can enumerate who would see one */
  allow_visible?: boolean
  /** The guild the channel is in. An interaction whose guild is not this one does not resolve */
  discord_guild_id: string
}

export type BoardEntry = {
  change_reason: string | null
  /** RFC 3339 with microsecond precision, always UTC. */
  computed_at: string
  confidence: 'unknown' | 'low' | 'medium' | 'high'
  contest_reason: string | null
  contested: boolean
  /** RFC 3339 with microsecond precision, always UTC. */
  died_at: string
  evidence: EvidenceCounts
  server: string
  status: 'unknown' | 'no_timer' | 'pre_window' | 'in_window' | 'overdue' | 'up'
  target: Target
  timer_source: 'circle_override' | 'catalogue' | 'none'
  /** RFC 3339 with microsecond precision, always UTC. */
  up_since: string
  window: Window
}

export type CatalogueEntry = {
  /** Every spelling that resolves to this target */
  aliases: Array<string> | null
  /** The instance-wide timer for the requested server. NOT the circle's effective timer: a circle override sits above it */
  catalogue_timer: TargetTimer
  category: 'open_world' | 'zone_boss' | 'planar' | 'ntov' | 'sleeper' | 'key_holder'
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  expansion: 'classic' | 'kunark' | 'velious'
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  /** Whether a server-wide repop resets this target */
  is_quake_target: boolean
  /** The canonical spelling, punctuation included */
  name: string
  name_norm: string
  state: 'active' | 'retired'
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  zone: string
  zone_norm: string
}

export type Circle = {
  accepted_providers: Array<ProviderView> | null
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  description: string
  disabled_providers: Array<string> | null
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  min_reporters_to_supersede: number
  name: string
  revocation_strength: string
  revocation_weak_reasons: Array<string> | null
  revoke_invalidates_invites: boolean
  server: string
  state: string
  timezone: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  weak_providers: Array<string> | null
}

export type CircleResponse = {
  accepted_providers: Array<ProviderView> | null
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  description: string
  disabled_providers: Array<string> | null
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  min_reporters_to_supersede: number
  name: string
  revocation_strength: string
  revocation_weak_reasons: Array<string> | null
  revoke_invalidates_invites: boolean
  server: string
  state: string
  timezone: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  weak_providers: Array<string> | null
}

export type ClientBody = {
  /** e.g. nparse-plus-tod */
  name?: string
  version?: string
}

export type CreateAuthorizationURLInputBody = {
  /** The invite code, if this is a join rather than a re-auth. A circle is resolved from this and from nothing else */
  invite_code?: string
  /** A provider key from listIdentityProviders */
  provider: string
}

export type CreateCircleInputBody = {
  /** Free text */
  description?: string
  /** The circle's name, unique per server */
  name: string
  /** blue, green or red. Immutable after creation */
  server: 'blue' | 'green' | 'red'
  /** IANA timezone, display only. Defaults to UTC */
  timezone?: string
}

export type CreateIdentityProviderInputBody = {
  /** Required to ENABLE a provider with no verifiable subject: revocation through one is advisory */
  acknowledge_weak_revocation?: boolean
  /** OIDC only */
  authorization_endpoint?: string
  /** The operator's own OAuth application. Required for discord and oidc, forbidden for local */
  client_id?: string
  /** Write-only: it is never returned by any operation */
  client_secret?: string
  /** What a join page calls it. Defaults to the key */
  display_name?: string
  /** Defaults to false, so a half-configured application is never briefly live */
  enabled?: boolean
  /** OIDC only: the issuer, an absolute https URL */
  issuer?: string
  /** OIDC only: where the signing keys live */
  jwks_uri?: string
  /** The wire key /join dispatches on */
  key: string
  /** discord, oidc or local. Immutable: it decides verifiable_subject */
  kind: 'discord' | 'oidc' | 'local'
  /** Where the provider sends the browser back */
  redirect_uri?: string
  /** OIDC only: the claim that carries the subject. Defaults to sub */
  subject_claim?: string
  /** Where the authorization code is exchanged */
  token_endpoint?: string
}

export type CreateInviteInputBody = {
  /** Defaults to 7 days, maximum 30. Clamped to 24 hours for a token */
  expires_in_seconds?: number
  /** Defaults to 1. Clamped to 1 for a token, and for a circle that accepts a weak provider */
  max_uses?: number
  /** Free text for the invite list */
  note?: string
  /** Defaults to member. Never owner */
  role?: 'officer' | 'member' | 'observer'
}

export type CreateRaidTargetInputBody = {
  /** Short forms raiders type. Every one must be unique across the whole catalogue */
  aliases?: Array<string> | null
  category: 'open_world' | 'zone_boss' | 'planar' | 'ntov' | 'sleeper' | 'key_holder'
  expansion: 'classic' | 'kunark' | 'velious'
  /** Whether a server-wide repop resets this target */
  is_quake_target?: boolean
  /** The canonical spelling, punctuation included */
  name: string
  zone: string
}

export type CreateServiceMemberInputBody = {
  /** The token's device name */
  client_name?: string
  /** What the member list calls this bot */
  display_name: string
  /** The responsible human. Defaults to the caller */
  owner_membership_id?: string
  /** Defaults to member. Never owner */
  role?: 'officer' | 'member' | 'observer'
  /** Narrow the token. Empty means every scope, still bounded by the role */
  scopes?: Array<string> | null
}

export type CreateTodReportInputBody = {
  /** The plugin's own skew estimate */
  client_clock_offset_seconds?: number
  /** Game truth. May be backdated; may not be in the future beyond 120s of skew */
  died_at: string
  /** Self-asserted; the intel officers actually want */
  killed_by_guild?: string
  /** Whose log file it came from */
  log_character?: string
  /** How sure the reporter is. Defaults to certain */
  self_confidence?: 'certain' | 'probable' | 'guess'
  /** Must be the circle's server. A mismatch is 422 server_mismatch */
  server: 'blue' | 'green' | 'red'
  /** Where the time came from. Defaults to manual */
  source?: 'log_line' | 'manual' | 'api' | 'import'
  /** The character named in the line */
  source_character?: string
  /** The raw log line, verbatim */
  source_line?: string
  /** The target. Exactly one of target_id and target_name */
  target_id?: string
  /** A parsed or hand-typed name; runs the resolve ladder */
  target_name?: string
}

export type CredentialBody = {
  id_token?: string
  /** provider_ticket, bearer_token, id_token or none */
  kind: 'provider_ticket' | 'bearer_token' | 'id_token' | 'none'
  nonce?: string
  ticket?: string
  token?: string
}

export type DiscordChannelBinding = {
  /** Whether a reply here may be posted where the channel can read it. Discord has no channel-membership API, so this server cannot know who that is */
  allow_visible: boolean
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  created_by_membership_id: string
  /** The Discord channel. One channel resolves to at most one circle */
  discord_channel_id: string
  /** The guild the binding was made in. An interaction from another guild does not resolve */
  discord_guild_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
}

export type DiscordChannelBindingResponse = {
  /** Whether a reply here may be posted where the channel can read it. Discord has no channel-membership API, so this server cannot know who that is */
  allow_visible: boolean
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  created_by_membership_id: string
  /** The Discord channel. One channel resolves to at most one circle */
  discord_channel_id: string
  /** The guild the binding was made in. An interaction from another guild does not resolve */
  discord_guild_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
}

export type Evidence = {
  distinct_reporter_count: number
  log_line_count: number
  report_count: number
  report_ids: Array<string> | null
  revoked_reporter_count: number
  spread_seconds: number
}

export type EvidenceCounts = {
  distinct_reporter_count: number
  log_line_count: number
  report_count: number
  revoked_reporter_count: number
  spread_seconds: number | null
}

export type Field = {
  /** Dotted path to the part of the request that was rejected */
  location: string
  /** What was wrong with it */
  message: string
}

export type InstanceSettingChange = {
  by_console: boolean
  /** RFC 3339 with microsecond precision, always UTC. */
  changed_at: string
  changed_by_identity_id: string
  id: string
  new_value: string
  old_value: string
  reason: string
  /** Which instance setting a ledger row is about. */
  setting: 'self_service_circle_creation' | 'name' | 'timezone'
}

export type InstanceSettingsResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** Every recorded change, newest first */
  changes: Array<InstanceSettingChange> | null
  name: string
  /** Read-only: it must keep matching every registered redirect URI. Sending it is 422 field_immutable */
  public_url: string
  /** The settings ledger's chain head. Empty on an instance nothing has changed */
  revision: string
  /** The instance's stated policy on who may create a circle. Published, not yet enforced: createCircle still requires instance.circle.create */
  self_service_circle_creation: boolean
  timezone: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
}

export type InteractionReply = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  data?: InteractionReplyData
  type: number
}

export type InteractionReplyData = {
  content: string
  flags: number
}

export type Invite = {
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  code_prefix: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  created_by_membership_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  live: boolean
  max_uses: number
  minted_by_kind: string
  note: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  role: string
  uses: number
}

export type InvitePreview = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  circle: InvitePreviewCircle
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  granted_role: string
  /** invite, or owner_grant for the code the CLI prints on first run */
  kind: string
  max_uses: number
  providers: Array<ProviderView> | null
  revocation_strength: string
  revocation_weak_reasons: Array<string> | null
  uses: number
  weak_providers: Array<string> | null
}

export type InvitePreviewCircle = {
  name: string
  server: string
}

export type InviteResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  code_prefix: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  created_by_membership_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  live: boolean
  max_uses: number
  minted_by_kind: string
  note: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  role: string
  uses: number
}

export type Item = {
  /** The guild this circle requires membership of */
  discord_guild_id?: string
  /** Any one of these roles admits. Empty means anyone in the guild */
  discord_required_role_ids?: Array<string> | null
  /** The provider's wire key, from listIdentityProviders */
  key: string
}

export type Joined = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  circle: Circle
  created: boolean
  membership: Member
  replayed?: boolean
  token: Token
}

export type Member = {
  admitted_by_invite_id?: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  display_name: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  identity_id?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  joined_at: string
  kind: string
  owner_membership_id?: string
  possible_duplicate: boolean
  provider_key?: string
  revocation_strength: string
  revocation_weak_reasons: Array<string> | null
  revoke_reason?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  role: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  weak_providers: Array<string> | null
}

export type MemberResponse = {
  admitted_by_invite_id?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  display_name: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  identity_id?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  joined_at: string
  kind: string
  owner_membership_id?: string
  possible_duplicate: boolean
  provider_key?: string
  revocation_strength: string
  revocation_weak_reasons: Array<string> | null
  revoke_reason?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  role: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  weak_providers: Array<string> | null
}

export type Meta = {
  /** Ambiguous resolve candidates */
  candidates?: unknown
  /** What narrowed the request below what it asked for */
  capped_by?: string
  /** The resource as it is now, on a 412 */
  current?: unknown
  /** Correlates this response with the instance log */
  request_id?: string
  /** Seconds to wait before retrying */
  retry_after_seconds?: number
  /** Which step-up tier the operation asks for */
  step_up_tier?: string
  /** Required re-authentication recency */
  step_up_window_seconds?: number
}

export type MintedInviteResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  capped_by?: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  code: string
  code_prefix: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  created_by_membership_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  live: boolean
  max_uses: number
  minted_by_kind: string
  note: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  role: string
  uses: number
}

export type OverrideResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  cluster_epsilon_seconds: number | null
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  created_by_membership_id: string
  fixed_grace_seconds: number
  note: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  target_id: string
  /** The target's canonical name, so a list of overrides is readable */
  target_name: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  window_close_offset_seconds: number | null
  window_kind: 'fixed' | 'variance' | 'unknown'
  window_open_offset_seconds: number | null
}

export type PageAdminIdentityProvider = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<AdminIdentityProvider> | null
  next_cursor: string
}

export type PageBoardEntry = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<BoardEntry> | null
  next_cursor: string
}

export type PageCatalogueEntry = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<CatalogueEntry> | null
  next_cursor: string
}

export type PageCircle = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<Circle> | null
  next_cursor: string
}

export type PageDiscordChannelBinding = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<DiscordChannelBinding> | null
  next_cursor: string
}

export type PageInvite = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<Invite> | null
  next_cursor: string
}

export type PageMember = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<Member> | null
  next_cursor: string
}

export type PagePublicIdentityProvider = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<PublicIdentityProvider> | null
  next_cursor: string
}

export type PageQuake = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<Quake> | null
  next_cursor: string
}

export type PageRecord = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<Record> | null
  next_cursor: string
}

export type PageReport = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<Report> | null
  next_cursor: string
}

export type PageTimerOverride = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<TimerOverride> | null
  next_cursor: string
}

export type PageTokenView = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  has_more: boolean
  items: Array<TokenView> | null
  next_cursor: string
}

export type PreviewInviteInputBody = {
  /** The invite code, in any case, with or without the TODI- prefix */
  code: string
}

export type PrincipalView = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  display_name: string
  kind: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  membership_id: string
  permissions: Array<string> | null
  role: string
  scopes: Array<string> | null
  step_up: Array<StepUpTierView> | null
  step_up_window_seconds: number
  stepped_up: boolean
  /** RFC 3339 with microsecond precision, always UTC. */
  token_expires_at: string
  token_prefix: string
}

export type Problem = {
  /** Stable machine-readable code from a closed enum */
  code: 'malformed_request' | 'unauthenticated' | 'token_invalid' | 'token_expired' | 'forbidden' | 'insufficient_scope' | 'session_required' | 'step_up_required' | 'not_found' | 'method_not_allowed' | 'not_acceptable' | 'request_timeout' | 'conflict' | 'precondition_failed' | 'payload_too_large' | 'unsupported_media_type' | 'validation_failed' | 'precondition_required' | 'idempotency_key_required' | 'idempotency_key_reused' | 'idempotency_conflict' | 'rate_limited' | 'internal_error' | 'service_unavailable' | 'membership_revoked' | 'invite_invalid' | 'invite_expired' | 'invite_exhausted' | 'invite_revoked' | 'provider_not_accepted' | 'provider_disabled' | 'provider_unverifiable' | 'credential_invalid' | 'credential_expired' | 'credential_stale' | 'identity_provider_unreachable' | 'acknowledgement_required' | 'server_mismatch' | 'died_at_in_future' | 'died_at_too_old' | 'already_retracted' | 'retract_not_permitted' | 'unknown_target' | 'ambiguous_target' | 'last_owner' | 'field_immutable' | 'link_requires_verifiable_identity' | 'guild_membership_required' | 'guild_role_required' | 'auth_ticket_invalid' | 'auth_ticket_expired' | 'auth_flow_expired' | 'identity_blocked' | 'credential_audience_mismatch' | 'provider_scope_declined'
  /** Explanation specific to this occurrence */
  detail?: string
  /** Per-field detail, where the failure has any */
  errors?: Array<Field> | null
  /** The request this problem is about */
  instance?: string
  /** Structured extras this problem needs */
  meta?: Meta
  /** HTTP status code */
  status: number
  /** Short summary of the problem type, identical on every occurrence */
  title: string
  /** Documentation for this error code; the last segment is the code */
  type: string
}

export type ProviderView = {
  available: boolean
  discord_guild_id?: string
  discord_required_role_ids: Array<string> | null
  display_name: string
  key: string
  kind: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  provider_id: string
  verifiable_subject: boolean
}

export type PublicIdentityProvider = {
  authorization_endpoint?: string
  browser_flow: boolean
  client_id?: string
  display_name: string
  issuer?: string
  key: string
  kind: 'discord' | 'oidc' | 'local'
  verifiable_subject: boolean
}

export type PutCircleTimerOverrideInputBody = {
  /** Per-target clustering window. Null derives it */
  cluster_epsilon_seconds?: number
  /** How long a fixed spawn stays in_window. Defaults to 900 */
  fixed_grace_seconds?: number
  /** Why these numbers, and who disputes them */
  note?: string
  /** Seconds from the ToD to the latest possible spawn. Equal to the open offset iff window_kind is fixed */
  window_close_offset_seconds?: number
  window_kind: 'fixed' | 'variance' | 'unknown'
  /** Seconds from the ToD to the earliest possible spawn. Null iff window_kind is unknown */
  window_open_offset_seconds?: number
}

export type PutRaidTargetTimerInputBody = {
  /** Per-target clustering window. Null derives it */
  cluster_epsilon_seconds?: number
  /** How long a fixed spawn stays in_window. Defaults to 900 */
  fixed_grace_seconds?: number
  /** Why these numbers, and who disputes them */
  note?: string
  /** Where these numbers came from. They are not ours and they are disputed */
  source?: string
  /** Seconds from the ToD to the latest possible spawn. Equal to the open offset iff window_kind is fixed */
  window_close_offset_seconds?: number
  window_kind: 'fixed' | 'variance' | 'unknown'
  /** Seconds from the ToD to the earliest possible spawn. Null iff window_kind is unknown */
  window_open_offset_seconds?: number
}

export type Quake = {
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  note?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  occurred_at: string
  /** RFC 3339 with microsecond precision, always UTC. */
  reported_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  reported_by_membership_id: string
  reporter_revoked: boolean
  source: string
}

export type QuakeResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  note?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  occurred_at: string
  /** RFC 3339 with microsecond precision, always UTC. */
  reported_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  reported_by_membership_id: string
  reporter_revoked: boolean
  source: string
}

export type Record = {
  action: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  actor_membership_id: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  detail: { [key: string]: unknown }
  entity_id?: string
  entity_type: string
  hash: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  prev_hash?: string
}

export type RedeemInviteInputBody = {
  client?: ClientBody
  credential: CredentialBody
  /** Required for local, optional elsewhere, where it overrides what the provider reported */
  display_name?: string
  /** The invite code, in any case, with or without the TODI- prefix */
  invite_code: string
  /** A provider key from previewInvite's providers[] */
  provider: string
  /** Narrow the minted token. Empty means every scope, still bounded by the role */
  scopes?: Array<string> | null
}

export type Report = {
  client_clock_offset_seconds: number | null
  /** RFC 3339 with microsecond precision, always UTC. */
  died_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  killed_by_guild?: string
  kind: string
  log_character?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  reported_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  reporter_membership_id: string
  reporter_revoked: boolean
  retracted: boolean
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  retracts_report_id: string
  self_confidence: string
  source: string
  source_character?: string
  source_line?: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  target_id: string
}

export type ReportQuakeInputBody = {
  /** Free text for the officers who read this later */
  note?: string
  /** Game truth, may be backdated. Defaults to now */
  occurred_at?: string
  /** Where it came from. Defaults to manual */
  source?: 'log_line' | 'manual' | 'api' | 'import'
}

export type Reporter = {
  display_name: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  membership_id: string
  revoked: boolean
}

export type ResolutionResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  candidates: Array<Target> | null
  match_kind: string
  target: Target
}

export type ResolveRaidTargetInputBody = {
  /** A name as somebody typed it: wrong case, missing backtick and stray whitespace are all fine */
  name: string
}

export type RetractTodReportInputBody = {
  /** Why, kept on the retraction row */
  reason?: string
}

export type RetractionResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  original: Report
  retraction: Report
}

export type RevokeMemberInputBody = {
  /** Why, for the audit log */
  reason?: string
}

export type RevokedResponse = {
  active_invite_count: number
  admitted_by_invite_id?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  display_name: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  identity_id?: string
  invites_revoked: number
  /** RFC 3339 with microsecond precision, always UTC. */
  joined_at: string
  kind: string
  owner_membership_id?: string
  possible_duplicate: boolean
  provider_key?: string
  revocation_strength: string
  revocation_weak_reasons: Array<string> | null
  revoke_reason?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  role: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  weak_providers: Array<string> | null
}

export type RunSetupInputBody = {
  circle: SetupCircleRequest
  /** The instance's name */
  name: string
  provider: SetupProviderRequest
  /** Where this instance is reachable. It must match the redirect URI registered with the identity provider EXACTLY */
  public_url?: string
  /** Let any authenticated principal create a circle */
  self_service_circle_creation?: boolean
  /** IANA timezone, display only. Defaults to UTC */
  timezone?: string
}

export type ServerMeta = {
  api_versions: Array<string> | null
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  configured: boolean
  name: string
  self_service_circle_creation: boolean
  setup_available: boolean
  version: string
}

export type ServiceMemberResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  circle: Circle
  membership: Member
  token: Token
}

export type SetCircleProvidersInputBody = {
  /** Required to accept a provider with no verifiable subject */
  acknowledge_weak_revocation?: boolean
  /** The complete set this circle accepts. Omitting one stops NEW joins through it and revokes nobody */
  providers: Array<Item> | null
}

export type SetupCircle = {
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  name: string
  server: string
}

export type SetupCircleRequest = {
  description?: string
  /** An EXISTING circle to issue the owner code for. Required once this instance has any circle */
  id?: string
  /** The first circle's name */
  name?: string
  /** blue, green or red. Immutable after creation */
  server?: 'blue' | 'green' | 'red'
  timezone?: string
}

export type SetupProvider = {
  display_name: string
  enabled: boolean
  key: string
  kind: string
  verifiable_subject: boolean
}

export type SetupProviderRequest = {
  /** Required for the local provider: revocation through a provider with no verifiable subject does not stop a revoked member rejoining under a new name */
  acknowledge_weak_revocation?: boolean
  /** OIDC only */
  authorization_endpoint?: string
  /** The operator's own OAuth application. Required for discord and oidc, forbidden for local */
  client_id?: string
  /** Write-only: it is never returned by any operation */
  client_secret?: string
  /** What the join page calls it */
  display_name?: string
  /** OIDC only */
  issuer?: string
  /** OIDC only */
  jwks_uri?: string
  /** The wire key /join dispatches on */
  key: string
  /** discord, oidc or local. Immutable after this: it decides verifiable_subject */
  kind: 'discord' | 'oidc' | 'local'
  redirect_uri?: string
  /** OIDC only. Defaults to sub */
  subject_claim?: string
  token_endpoint?: string
}

export type SetupResult = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  circle_id: string
  circle_name: string
  join_path: string
  owner_code: string
  /** RFC 3339 with microsecond precision, always UTC. */
  owner_code_expires_at: string
  raid_targets_added: number
  raid_targets_present: number
  revocation_strength: string
  steps: Array<SetupStep> | null
}

export type SetupState = {
  administrator_exists: boolean
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  available: boolean
  circles: Array<SetupCircle> | null
  configured: boolean
  instance_name: string
  providers: Array<SetupProvider> | null
  public_url: string
  raid_targets: number
  self_service_circle_creation: boolean
  timezone: string
}

export type SetupStep = {
  detail?: string
  name: string
  /** created, updated or already_present */
  outcome: string
}

export type SignOutResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** Live personal access tokens this membership still holds. Signing out never revokes one */
  tokens_kept: number
}

export type StepUpResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  step_up: Array<StepUpTierView> | null
  /** RFC 3339 with microsecond precision, always UTC. */
  stepped_up_at: string
  /** Always false: re-proving a session mints no personal access token */
  token_minted: boolean
}

export type StepUpSessionInputBody = {
  credential: CredentialBody
  /** Required for local; used to verify, never to rename */
  display_name?: string
  /** A provider key this circle accepts */
  provider: string
}

export type StepUpTierView = {
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  satisfied: boolean
  /** routine or sensitive */
  tier: string
  window_seconds: number
}

export type Target = {
  /** Every spelling that resolves to this target */
  aliases: Array<string> | null
  category: 'open_world' | 'zone_boss' | 'planar' | 'ntov' | 'sleeper' | 'key_holder'
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  expansion: 'classic' | 'kunark' | 'velious'
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  /** Whether a server-wide repop resets this target */
  is_quake_target: boolean
  /** The canonical spelling, punctuation included */
  name: string
  name_norm: string
  state: 'active' | 'retired'
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  zone: string
  zone_norm: string
}

export type TargetResponse = {
  /** Every spelling that resolves to this target */
  aliases: Array<string> | null
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  category: 'open_world' | 'zone_boss' | 'planar' | 'ntov' | 'sleeper' | 'key_holder'
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  expansion: 'classic' | 'kunark' | 'velious'
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  /** Whether a server-wide repop resets this target */
  is_quake_target: boolean
  /** The canonical spelling, punctuation included */
  name: string
  name_norm: string
  state: 'active' | 'retired'
  timers: Array<TargetTimer> | null
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  zone: string
  zone_norm: string
}

export type TargetStateResponse = {
  alternatives: Array<Alternative> | null
  alternatives_total: number
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  attribution_visible: boolean
  change_reason: string | null
  /** RFC 3339 with microsecond precision, always UTC. */
  computed_at: string
  confidence: 'unknown' | 'low' | 'medium' | 'high'
  contest_reason: string | null
  contested: boolean
  /** RFC 3339 with microsecond precision, always UTC. */
  died_at: string
  evidence: Evidence
  implausible_report_ids: Array<string> | null
  reporters?: Array<Reporter> | null
  server: string
  status: 'unknown' | 'no_timer' | 'pre_window' | 'in_window' | 'overdue' | 'up'
  target: Target
  timer_source: 'circle_override' | 'catalogue' | 'none'
  /** RFC 3339 with microsecond precision, always UTC. */
  up_since: string
  window: Window
}

export type TargetTimer = {
  cluster_epsilon_seconds: number | null
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  fixed_grace_seconds: number
  note: string
  server: 'blue' | 'green' | 'red'
  source: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  window_close_offset_seconds: number | null
  window_kind: 'fixed' | 'variance' | 'unknown'
  window_open_offset_seconds: number | null
}

export type TimerOverride = {
  cluster_epsilon_seconds: number | null
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  created_by_membership_id: string
  fixed_grace_seconds: number
  note: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  target_id: string
  /** The target's canonical name, so a list of overrides is readable */
  target_name: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  window_close_offset_seconds: number | null
  window_kind: 'fixed' | 'variance' | 'unknown'
  window_open_offset_seconds: number | null
}

export type TimerResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  cluster_epsilon_seconds: number | null
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  fixed_grace_seconds: number
  note: string
  server: 'blue' | 'green' | 'red'
  source: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  target_id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  updated_at: string
  window_close_offset_seconds: number | null
  window_kind: 'fixed' | 'variance' | 'unknown'
  window_open_offset_seconds: number | null
}

export type TodReportResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  client_clock_offset_seconds: number | null
  /** RFC 3339 with microsecond precision, always UTC. */
  died_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  killed_by_guild?: string
  kind: string
  log_character?: string
  /** RFC 3339 with microsecond precision, always UTC. */
  reported_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  reporter_membership_id: string
  reporter_revoked: boolean
  retracted: boolean
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  retracts_report_id: string
  self_confidence: string
  source: string
  source_character?: string
  source_line?: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  target_id: string
}

export type Token = {
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  name: string
  scopes: Array<string> | null
  token: string
  token_prefix: string
}

export type TokenResponse = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  last_used_at: string
  name: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  scopes: Array<string> | null
  token_prefix: string
}

export type TokenView = {
  /** RFC 3339 with microsecond precision, always UTC. */
  created_at: string
  /** RFC 3339 with microsecond precision, always UTC. */
  expires_at: string
  /** A ULID: 26 characters of Crockford base32, lexicographically time-ordered. */
  id: string
  /** RFC 3339 with microsecond precision, always UTC. */
  last_used_at: string
  name: string
  /** RFC 3339 with microsecond precision, always UTC. */
  revoked_at: string
  scopes: Array<string> | null
  token_prefix: string
}

export type UnbindCircleDiscordChannelOutputBody = {
  /** RFC 3339 with microsecond precision, always UTC. */
  as_of: string
  discord_channel_id: string
}

export type UpdateCircleInputBody = {
  description?: string
  min_reporters_to_supersede?: number
  name?: string
  /** Owner only: it decides whether revoking a weakly-revocable member also kills the circle's live invites */
  revoke_invalidates_invites?: boolean
  /** Rejected with 422 field_immutable; a circle is pinned to one server */
  server?: string
  state?: 'active' | 'archived'
  timezone?: string
}

export type UpdateIdentityProviderInputBody = {
  /** Required when this change ENABLES a provider with no verifiable subject */
  acknowledge_weak_revocation?: boolean
  authorization_endpoint?: string
  client_id?: string
  /** Write-only. Send it to rotate; omit it to leave the stored one alone */
  client_secret?: string
  display_name?: string
  enabled?: boolean
  issuer?: string
  jwks_uri?: string
  /** Rejected with 422 field_immutable */
  key?: string
  /** Rejected with 422 field_immutable: kind decides verifiable_subject */
  kind?: string
  redirect_uri?: string
  subject_claim?: string
  token_endpoint?: string
}

export type UpdateInstanceSettingsInputBody = {
  name?: string
  /** Rejected with 422 field_immutable: it must keep matching every registered redirect URI */
  public_url?: string
  /** Why, recorded in the hash-chained ledger and shown in every listing */
  reason?: string
  /** Let any authenticated principal create a circle */
  self_service_circle_creation?: boolean
  /** IANA timezone, display only */
  timezone?: string
}

export type UpdateMemberInputBody = {
  display_name?: string
  role?: 'owner' | 'officer' | 'member' | 'observer'
}

export type UpdateRaidTargetInputBody = {
  /** REPLACES the alias set. Sending [] removes every alias */
  aliases?: Array<string>
  category?: 'open_world' | 'zone_boss' | 'planar' | 'ntov' | 'sleeper' | 'key_holder'
  expansion?: 'classic' | 'kunark' | 'velious'
  is_quake_target?: boolean
  name?: string
  state?: 'active' | 'retired'
  zone?: string
}

export type Window = {
  /** RFC 3339 with microsecond precision, always UTC. */
  close_at: string
  kind: string
  /** RFC 3339 with microsecond precision, always UTC. */
  open_at: string
  progress_bp: number | null
  seconds_until_close: number | null
  seconds_until_open: number | null
  /** RFC 3339 with microsecond precision, always UTC. */
  spawn_at: string
}

/** OperationId is every operation the published document carries. */
export type OperationId =
  | 'authenticateIdentity'
  | 'bindCircleDiscordChannel'
  | 'createAuthorizationURL'
  | 'createCircle'
  | 'createIdentityProvider'
  | 'createInvite'
  | 'createRaidTarget'
  | 'createServiceMember'
  | 'createTodReport'
  | 'deleteCircle'
  | 'deleteCircleTimerOverride'
  | 'deleteIdentityProvider'
  | 'getCircle'
  | 'getCircleDiscordChannel'
  | 'getCurrentPrincipal'
  | 'getInstanceSettings'
  | 'getMember'
  | 'getRaidTarget'
  | 'getServerMeta'
  | 'getSetupState'
  | 'getTargetState'
  | 'getTodReport'
  | 'handleDiscordInteraction'
  | 'listAdminIdentityProviders'
  | 'listCircleAudit'
  | 'listCircleDiscordChannels'
  | 'listCircles'
  | 'listCircleTimerOverrides'
  | 'listIdentityProviders'
  | 'listInvites'
  | 'listMembers'
  | 'listMyTokens'
  | 'listQuakes'
  | 'listRaidTargets'
  | 'listTargetStates'
  | 'listTodReports'
  | 'previewInvite'
  | 'putCircleTimerOverride'
  | 'putRaidTargetTimer'
  | 'redeemInvite'
  | 'reinstateMember'
  | 'reportQuake'
  | 'resolveRaidTarget'
  | 'retractTodReport'
  | 'revokeInvite'
  | 'revokeMember'
  | 'revokeToken'
  | 'runSetup'
  | 'setCircleProviders'
  | 'signOut'
  | 'stepUpSession'
  | 'unbindCircleDiscordChannel'
  | 'updateCircle'
  | 'updateIdentityProvider'
  | 'updateInstanceSettings'
  | 'updateMember'
  | 'updateRaidTarget'

/**
 * OperationSpec is what the route registry declares about an operation, carried through the
 * document so the console reads the same facts the middleware enforces.
 *
 * `sessionOnly` and `stepUp` are here so the console can SAY it is stepping up rather than
 * letting a 403 arrive with nothing to explain it.
 */
export interface OperationSpec {
  readonly id: OperationId
  readonly method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  readonly path: string
  readonly pathParams: readonly string[]
  readonly queryParams: readonly string[]
  /** The PAT scopes that reach it. Empty means no token does, at any scope. */
  readonly scopes: readonly string[]
  /** No personal access token reaches this operation; a browser session is the only credential. */
  readonly sessionOnly: boolean
  /** The capability floor: session only, and recently re-authenticated. */
  readonly stepUp: boolean
  readonly circleScoped: boolean
  /** Non-empty when `Idempotency-Key` is required. */
  readonly idempotency: '' | 'membership' | 'handler'
  /** The operation returns an entity tag. */
  readonly etag: boolean
  /** `If-Match` is required: the operation overwrites state a previous read supplied. */
  readonly ifMatch: boolean
}

export const OPERATIONS = {
  authenticateIdentity: {
    id: 'authenticateIdentity',
    method: 'POST',
    path: '/api/v1/sessions',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: 'handler',
    etag: false,
    ifMatch: false,
  },
  bindCircleDiscordChannel: {
    id: 'bindCircleDiscordChannel',
    method: 'PUT',
    path: '/api/v1/circles/{circle_id}/discord-channels/{discord_channel_id}',
    pathParams: ['circle_id', 'discord_channel_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  createAuthorizationURL: {
    id: 'createAuthorizationURL',
    method: 'POST',
    path: '/api/v1/auth/authorization-url',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  createCircle: {
    id: 'createCircle',
    method: 'POST',
    path: '/api/v1/circles',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: 'handler',
    etag: false,
    ifMatch: false,
  },
  createIdentityProvider: {
    id: 'createIdentityProvider',
    method: 'POST',
    path: '/api/v1/admin/identity-providers',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: 'handler',
    etag: false,
    ifMatch: false,
  },
  createInvite: {
    id: 'createInvite',
    method: 'POST',
    path: '/api/v1/circles/{circle_id}/invites',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: ['invite:create'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: 'membership',
    etag: false,
    ifMatch: false,
  },
  createRaidTarget: {
    id: 'createRaidTarget',
    method: 'POST',
    path: '/api/v1/raid-targets',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: 'handler',
    etag: false,
    ifMatch: false,
  },
  createServiceMember: {
    id: 'createServiceMember',
    method: 'POST',
    path: '/api/v1/circles/{circle_id}/service-members',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: 'membership',
    etag: false,
    ifMatch: false,
  },
  createTodReport: {
    id: 'createTodReport',
    method: 'POST',
    path: '/api/v1/circles/{circle_id}/tod-reports',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: ['tod:report'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: 'membership',
    etag: false,
    ifMatch: false,
  },
  deleteCircle: {
    id: 'deleteCircle',
    method: 'DELETE',
    path: '/api/v1/circles/{circle_id}',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  deleteCircleTimerOverride: {
    id: 'deleteCircleTimerOverride',
    method: 'DELETE',
    path: '/api/v1/circles/{circle_id}/timer-overrides/{target_id}',
    pathParams: ['circle_id', 'target_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  deleteIdentityProvider: {
    id: 'deleteIdentityProvider',
    method: 'DELETE',
    path: '/api/v1/admin/identity-providers/{provider_id}',
    pathParams: ['provider_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  getCircle: {
    id: 'getCircle',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: ['circle:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: false,
  },
  getCircleDiscordChannel: {
    id: 'getCircleDiscordChannel',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/discord-channels/{discord_channel_id}',
    pathParams: ['circle_id', 'discord_channel_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: false,
  },
  getCurrentPrincipal: {
    id: 'getCurrentPrincipal',
    method: 'GET',
    path: '/api/v1/me',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  getInstanceSettings: {
    id: 'getInstanceSettings',
    method: 'GET',
    path: '/api/v1/admin/instance',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: '',
    etag: true,
    ifMatch: false,
  },
  getMember: {
    id: 'getMember',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/members/{member_id}',
    pathParams: ['circle_id', 'member_id'],
    queryParams: [],
    scopes: ['member:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: false,
  },
  getRaidTarget: {
    id: 'getRaidTarget',
    method: 'GET',
    path: '/api/v1/raid-targets/{target_id}',
    pathParams: ['target_id'],
    queryParams: [],
    scopes: ['catalogue:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: true,
    ifMatch: false,
  },
  getServerMeta: {
    id: 'getServerMeta',
    method: 'GET',
    path: '/api/v1/meta',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  getSetupState: {
    id: 'getSetupState',
    method: 'GET',
    path: '/api/v1/setup',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  getTargetState: {
    id: 'getTargetState',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/tods/{target_id}',
    pathParams: ['circle_id', 'target_id'],
    queryParams: [],
    scopes: ['tod:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: false,
  },
  getTodReport: {
    id: 'getTodReport',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/tod-reports/{report_id}',
    pathParams: ['circle_id', 'report_id'],
    queryParams: [],
    scopes: ['tod:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  handleDiscordInteraction: {
    id: 'handleDiscordInteraction',
    method: 'POST',
    path: '/api/v1/integrations/discord/interactions',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listAdminIdentityProviders: {
    id: 'listAdminIdentityProviders',
    method: 'GET',
    path: '/api/v1/admin/identity-providers',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listCircleAudit: {
    id: 'listCircleAudit',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/audit',
    pathParams: ['circle_id'],
    queryParams: ['cursor', 'limit'],
    scopes: [],
    sessionOnly: true,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listCircleDiscordChannels: {
    id: 'listCircleDiscordChannels',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/discord-channels',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listCircles: {
    id: 'listCircles',
    method: 'GET',
    path: '/api/v1/circles',
    pathParams: [],
    queryParams: [],
    scopes: ['circle:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listCircleTimerOverrides: {
    id: 'listCircleTimerOverrides',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/timer-overrides',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listIdentityProviders: {
    id: 'listIdentityProviders',
    method: 'GET',
    path: '/api/v1/identity-providers',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listInvites: {
    id: 'listInvites',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/invites',
    pathParams: ['circle_id'],
    queryParams: ['cursor', 'limit'],
    scopes: ['invite:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listMembers: {
    id: 'listMembers',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/members',
    pathParams: ['circle_id'],
    queryParams: ['cursor', 'limit'],
    scopes: ['member:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listMyTokens: {
    id: 'listMyTokens',
    method: 'GET',
    path: '/api/v1/tokens',
    pathParams: [],
    queryParams: ['cursor', 'limit'],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listQuakes: {
    id: 'listQuakes',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/quakes',
    pathParams: ['circle_id'],
    queryParams: ['cursor', 'limit'],
    scopes: ['tod:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listRaidTargets: {
    id: 'listRaidTargets',
    method: 'GET',
    path: '/api/v1/raid-targets',
    pathParams: [],
    queryParams: ['server', 'expansion', 'zone', 'q', 'include_retired', 'cursor', 'limit'],
    scopes: ['catalogue:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  listTargetStates: {
    id: 'listTargetStates',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/tods',
    pathParams: ['circle_id'],
    queryParams: ['cursor', 'limit', 'status', 'expansion', 'zone', 'contested', 'q'],
    scopes: ['tod:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: false,
  },
  listTodReports: {
    id: 'listTodReports',
    method: 'GET',
    path: '/api/v1/circles/{circle_id}/tod-reports',
    pathParams: ['circle_id'],
    queryParams: ['cursor', 'limit', 'target_id', 'died_after', 'died_before', 'reporter_membership_id', 'include_retracted'],
    scopes: ['tod:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  previewInvite: {
    id: 'previewInvite',
    method: 'POST',
    path: '/api/v1/invites/preview',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  putCircleTimerOverride: {
    id: 'putCircleTimerOverride',
    method: 'PUT',
    path: '/api/v1/circles/{circle_id}/timer-overrides/{target_id}',
    pathParams: ['circle_id', 'target_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  putRaidTargetTimer: {
    id: 'putRaidTargetTimer',
    method: 'PUT',
    path: '/api/v1/raid-targets/{target_id}/timers/{server}',
    pathParams: ['target_id', 'server'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  redeemInvite: {
    id: 'redeemInvite',
    method: 'POST',
    path: '/api/v1/join',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: 'handler',
    etag: false,
    ifMatch: false,
  },
  reinstateMember: {
    id: 'reinstateMember',
    method: 'POST',
    path: '/api/v1/circles/{circle_id}/members/{member_id}/reinstate',
    pathParams: ['circle_id', 'member_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: true,
  },
  reportQuake: {
    id: 'reportQuake',
    method: 'POST',
    path: '/api/v1/circles/{circle_id}/quakes',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: false,
    circleScoped: true,
    idempotency: 'membership',
    etag: false,
    ifMatch: false,
  },
  resolveRaidTarget: {
    id: 'resolveRaidTarget',
    method: 'POST',
    path: '/api/v1/raid-targets/resolve',
    pathParams: [],
    queryParams: [],
    scopes: ['catalogue:read'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  retractTodReport: {
    id: 'retractTodReport',
    method: 'POST',
    path: '/api/v1/circles/{circle_id}/tod-reports/{report_id}/retract',
    pathParams: ['circle_id', 'report_id'],
    queryParams: [],
    scopes: ['tod:retract'],
    sessionOnly: false,
    stepUp: false,
    circleScoped: true,
    idempotency: 'membership',
    etag: false,
    ifMatch: false,
  },
  revokeInvite: {
    id: 'revokeInvite',
    method: 'DELETE',
    path: '/api/v1/circles/{circle_id}/invites/{invite_id}',
    pathParams: ['circle_id', 'invite_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  revokeMember: {
    id: 'revokeMember',
    method: 'POST',
    path: '/api/v1/circles/{circle_id}/members/{member_id}/revoke',
    pathParams: ['circle_id', 'member_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: true,
  },
  revokeToken: {
    id: 'revokeToken',
    method: 'DELETE',
    path: '/api/v1/tokens/{token_id}',
    pathParams: ['token_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  runSetup: {
    id: 'runSetup',
    method: 'POST',
    path: '/api/v1/setup',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: false,
    stepUp: false,
    circleScoped: false,
    idempotency: 'handler',
    etag: false,
    ifMatch: false,
  },
  setCircleProviders: {
    id: 'setCircleProviders',
    method: 'PUT',
    path: '/api/v1/circles/{circle_id}/providers',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  signOut: {
    id: 'signOut',
    method: 'DELETE',
    path: '/api/v1/sessions',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  stepUpSession: {
    id: 'stepUpSession',
    method: 'POST',
    path: '/api/v1/sessions/step-up',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: false,
    circleScoped: false,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  unbindCircleDiscordChannel: {
    id: 'unbindCircleDiscordChannel',
    method: 'DELETE',
    path: '/api/v1/circles/{circle_id}/discord-channels/{discord_channel_id}',
    pathParams: ['circle_id', 'discord_channel_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: false,
    ifMatch: false,
  },
  updateCircle: {
    id: 'updateCircle',
    method: 'PATCH',
    path: '/api/v1/circles/{circle_id}',
    pathParams: ['circle_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  updateIdentityProvider: {
    id: 'updateIdentityProvider',
    method: 'PATCH',
    path: '/api/v1/admin/identity-providers/{provider_id}',
    pathParams: ['provider_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  updateInstanceSettings: {
    id: 'updateInstanceSettings',
    method: 'PATCH',
    path: '/api/v1/admin/instance',
    pathParams: [],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  updateMember: {
    id: 'updateMember',
    method: 'PATCH',
    path: '/api/v1/circles/{circle_id}/members/{member_id}',
    pathParams: ['circle_id', 'member_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: true,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
  updateRaidTarget: {
    id: 'updateRaidTarget',
    method: 'PATCH',
    path: '/api/v1/raid-targets/{target_id}',
    pathParams: ['target_id'],
    queryParams: [],
    scopes: [],
    sessionOnly: true,
    stepUp: true,
    circleScoped: false,
    idempotency: '',
    etag: true,
    ifMatch: true,
  },
} as const satisfies { [K in OperationId]: OperationSpec }

/** Re-authenticate an existing membership on a new device, with no invite */
export interface AuthenticateIdentityInput {
  body: AuthenticateIdentityInputBody
}
export type AuthenticateIdentityResult = Joined

/** Bind a Discord channel to this circle, and say whether replies there may be visible */
export interface BindCircleDiscordChannelInput {
  /** The circle */
  circle_id: string
  /** The Discord channel id, 1 to 20 digits */
  discord_channel_id: string
  body: BindCircleDiscordChannelInputBody
}
export type BindCircleDiscordChannelResult = DiscordChannelBindingResponse

/** Start a browser OAuth flow. Takes no circle_id, by design */
export interface CreateAuthorizationURLInput {
  body: CreateAuthorizationURLInputBody
}
export type CreateAuthorizationURLResult = AuthorizationStart

/** Create a circle on this instance */
export interface CreateCircleInput {
  body: CreateCircleInputBody
}
export type CreateCircleResult = CircleResponse

/** Add an identity provider */
export interface CreateIdentityProviderInput {
  body: CreateIdentityProviderInputBody
}
export type CreateIdentityProviderResult = AdminIdentityProviderResponse

/** Mint an invite code. One minted by a token is hard-narrowed to one use, 24 hours and a role below owner */
export interface CreateInviteInput {
  /** The circle */
  circle_id: string
  body: CreateInviteInputBody
}
export type CreateInviteResult = MintedInviteResponse

/** Add a raid target, for every circle on the instance */
export interface CreateRaidTargetInput {
  body: CreateRaidTargetInputBody
}
export type CreateRaidTargetResult = TargetResponse

/** Create a service membership and mint its token, owned by a named human */
export interface CreateServiceMemberInput {
  /** The circle */
  circle_id: string
  body: CreateServiceMemberInputBody
}
export type CreateServiceMemberResult = ServiceMemberResponse

/** Append one immutable time-of-death report */
export interface CreateTodReportInput {
  /** The circle */
  circle_id: string
  body: CreateTodReportInputBody
}
export type CreateTodReportResult = TodReportResponse

/** Delete the circle and every report in it */
export interface DeleteCircleInput {
  /** The circle */
  circle_id: string
}
export type DeleteCircleResult = CircleResponse

/** Remove a circle's timer override */
export interface DeleteCircleTimerOverrideInput {
  /** The circle */
  circle_id: string
  /** The raid target */
  target_id: string
}
export type DeleteCircleTimerOverrideResult = OverrideResponse

/** Remove an identity provider */
export interface DeleteIdentityProviderInput {
  /** The provider */
  provider_id: string
}
export type DeleteIdentityProviderResult = AdminIdentityProviderResponse

/** Read the circle */
export interface GetCircleInput {
  /** The circle */
  circle_id: string
}
export type GetCircleResult = CircleResponse

/** One Discord channel binding, and the ETag a change to it must quote back */
export interface GetCircleDiscordChannelInput {
  /** The circle */
  circle_id: string
  /** The Discord channel id */
  discord_channel_id: string
}
export type GetCircleDiscordChannelResult = DiscordChannelBindingResponse

/** The calling principal: membership, circle, role, effective permissions, token prefix, scopes, expiry */
export type GetCurrentPrincipalInput = EmptyInput
export type GetCurrentPrincipalResult = PrincipalView

/** The instance-wide settings, and the hash-chained ledger of every change to them */
export type GetInstanceSettingsInput = EmptyInput
export type GetInstanceSettingsResult = InstanceSettingsResponse

/** Read one member */
export interface GetMemberInput {
  /** The circle */
  circle_id: string
  /** The membership */
  member_id: string
}
export type GetMemberResult = MemberResponse

/** One raid target */
export interface GetRaidTargetInput {
  /** The raid target */
  target_id: string
}
export type GetRaidTargetResult = TargetResponse

/** Version, API versions, feature flags, and whether self-service circle creation is on */
export type GetServerMetaInput = EmptyInput
export type GetServerMetaResult = ServerMeta

/** What first-run setup has to work with: the instance row, providers, circles and catalogue */
export type GetSetupStateInput = EmptyInput
export type GetSetupStateResult = SetupState

/** One target: state, window, evidence and alternatives */
export interface GetTargetStateInput {
  /** The circle */
  circle_id: string
  /** The raid target */
  target_id: string
}
export type GetTargetStateResult = TargetStateResponse

/** One report. Reports are immutable, so this representation never changes */
export interface GetTodReportInput {
  /** The circle */
  circle_id: string
  /** The report */
  report_id: string
}
export type GetTodReportResult = TodReportResponse

/** Discord interactions endpoint. Verifies an Ed25519 signature; the circle comes from the channel binding */
export type HandleDiscordInteractionInput = EmptyInput
export type HandleDiscordInteractionResult = InteractionReply

/** The instance's identity providers, secrets excluded */
export type ListAdminIdentityProvidersInput = EmptyInput
export type ListAdminIdentityProvidersResult = PageAdminIdentityProvider

/** The circle's audit log */
export interface ListCircleAuditInput {
  /** The circle */
  circle_id: string
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
}
export type ListCircleAuditResult = PageRecord

/** The Discord channels bound to this circle, and which of them may be replied to visibly */
export interface ListCircleDiscordChannelsInput {
  /** The circle */
  circle_id: string
}
export type ListCircleDiscordChannelsResult = PageDiscordChannelBinding

/** The circles I am a member of. There is no list-all operation at any permission level */
export type ListCirclesInput = EmptyInput
export type ListCirclesResult = PageCircle

/** The circle's timer overrides */
export interface ListCircleTimerOverridesInput {
  /** The circle */
  circle_id: string
}
export type ListCircleTimerOverridesResult = PageTimerOverride

/** The enabled identity providers, and never a secret. Needed before auth */
export type ListIdentityProvidersInput = EmptyInput
export type ListIdentityProvidersResult = PagePublicIdentityProvider

/** List the circle's invites */
export interface ListInvitesInput {
  /** The circle */
  circle_id: string
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
}
export type ListInvitesResult = PageInvite

/** List the circle's members */
export interface ListMembersInput {
  /** The circle */
  circle_id: string
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
}
export type ListMembersResult = PageMember

/** My own devices. Officers see nobody's */
export interface ListMyTokensInput {
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
}
export type ListMyTokensResult = PageTokenView

/** The quake log */
export interface ListQuakesInput {
  /** The circle */
  circle_id: string
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
}
export type ListQuakesResult = PageQuake

/** The raid-target catalogue. Instance-wide: a mob's existence is a game fact */
export interface ListRaidTargetsInput {
  /** Fold this server's timer into each row */
  server?: 'blue' | 'green' | 'red'
  expansion?: 'classic' | 'kunark' | 'velious'
  /** Matched case- and punctuation-insensitively */
  zone?: string
  /** Substring of a target's name or one of its aliases */
  q?: string
  /** Include targets the server no longer spawns */
  include_retired?: boolean
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
}
export type ListRaidTargetsResult = PageCatalogueEntry

/** The board: every target's derived state, window and evidence */
export interface ListTargetStatesInput {
  /** The circle */
  circle_id: string
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
  /** Only targets in this state */
  status?: 'unknown' | 'no_timer' | 'pre_window' | 'in_window' | 'overdue' | 'up'
  /** Only targets from this expansion */
  expansion?: 'classic' | 'kunark' | 'velious'
  /** Only targets in this zone; matched normalised */
  zone?: string
  /** Only contested targets, or only uncontested ones */
  contested?: 'true' | 'false'
  /** Substring of a target's name or one of its aliases, matched normalised */
  q?: string
}
export type ListTargetStatesResult = PageBoardEntry

/** The report log, newest first, cursor-paginated */
export interface ListTodReportsInput {
  /** The circle */
  circle_id: string
  /** Opaque cursor from a previous page's next_cursor */
  cursor?: string
  /** Page size, 1-200 */
  limit?: number
  /** Only reports about this target */
  target_id?: string
  /** Only reports whose died_at is at or after this instant */
  died_after?: string
  /** Only reports whose died_at is at or before this instant */
  died_before?: string
  /** Only this member's reports */
  reporter_membership_id?: string
  /** Bring back retracted kills and the retractions naming them */
  include_retracted?: boolean
}
export type ListTodReportsResult = PageReport

/** Read an invite by code. The code travels in the body, never the path */
export interface PreviewInviteInput {
  body: PreviewInviteInputBody
}
export type PreviewInviteResult = InvitePreview

/** Override one target's timer for this circle */
export interface PutCircleTimerOverrideInput {
  /** The circle */
  circle_id: string
  /** The raid target this circle disagrees about */
  target_id: string
  body: PutCircleTimerOverrideInputBody
}
export type PutCircleTimerOverrideResult = OverrideResponse

/** Set a target's respawn timer for one server */
export interface PutRaidTargetTimerInput {
  /** The raid target */
  target_id: string
  /** The server this window is for */
  server: 'blue' | 'green' | 'red'
  body: PutRaidTargetTimerInputBody
}
export type PutRaidTargetTimerResult = TimerResponse

/** Redeem an invite: verify the credential, create the identity and membership, mint a token */
export interface RedeemInviteInput {
  body: RedeemInviteInputBody
}
export type RedeemInviteResult = Joined

/** Reinstate a revoked membership. The only way back in */
export interface ReinstateMemberInput {
  /** The circle */
  circle_id: string
  /** The membership */
  member_id: string
}
export type ReinstateMemberResult = MemberResponse

/** Record a server-wide earthquake. A false one wipes the whole board */
export interface ReportQuakeInput {
  /** The circle */
  circle_id: string
  body: ReportQuakeInputBody
}
export type ReportQuakeResult = QuakeResponse

/** Resolve a target name through the ladder: exact, normalised, alias, prefix, substring */
export interface ResolveRaidTargetInput {
  body: ResolveRaidTargetInputBody
}
export type ResolveRaidTargetResult = ResolutionResponse

/** Retract a report by appending a retraction row. The original stays visible */
export interface RetractTodReportInput {
  /** The circle */
  circle_id: string
  /** The report to retract */
  report_id: string
  body: RetractTodReportInputBody
}
export type RetractTodReportResult = RetractionResponse

/** Revoke an invite before it expires */
export interface RevokeInviteInput {
  /** The circle */
  circle_id: string
  /** The invite */
  invite_id: string
}
export type RevokeInviteResult = InviteResponse

/** Revoke a membership. Their reports still count */
export interface RevokeMemberInput {
  /** The circle */
  circle_id: string
  /** The membership */
  member_id: string
  body: RevokeMemberInputBody
}
export type RevokeMemberResult = RevokedResponse

/** Revoke one of my own devices */
export interface RevokeTokenInput {
  /** The token to revoke. Must be one of your own */
  token_id: string
}
export type RevokeTokenResult = TokenResponse

/** Create the instance, its first provider and its first circle, and return a one-time owner code */
export interface RunSetupInput {
  body: RunSetupInputBody
}
export type RunSetupResult = SetupResult

/** Set which identity providers the circle accepts, which changes its revocation strength */
export interface SetCircleProvidersInput {
  /** The circle */
  circle_id: string
  body: SetCircleProvidersInputBody
}
export type SetCircleProvidersResult = CircleResponse

/** End my own browser session. Ends this session only, and touches no token */
export type SignOutInput = EmptyInput
export type SignOutResult = SignOutResponse

/** Re-prove my identity for the session I already have. Mints no token and creates no device */
export interface StepUpSessionInput {
  body: StepUpSessionInputBody
}
export type StepUpSessionResult = StepUpResponse

/** Remove a channel binding. It stops the next reply and unsays nothing already posted */
export interface UnbindCircleDiscordChannelInput {
  /** The circle */
  circle_id: string
  /** The Discord channel id */
  discord_channel_id: string
}
export type UnbindCircleDiscordChannelResult = UnbindCircleDiscordChannelOutputBody

/** Rename the circle or change its settings. `server` is immutable */
export interface UpdateCircleInput {
  /** The circle */
  circle_id: string
  body: UpdateCircleInputBody
}
export type UpdateCircleResult = CircleResponse

/** Change an identity provider */
export interface UpdateIdentityProviderInput {
  /** The provider */
  provider_id: string
  body: UpdateIdentityProviderInputBody
}
export type UpdateIdentityProviderResult = AdminIdentityProviderResponse

/** Change the instance-wide settings, recording each change in the ledger */
export interface UpdateInstanceSettingsInput {
  body: UpdateInstanceSettingsInputBody
}
export type UpdateInstanceSettingsResult = InstanceSettingsResponse

/** Change a member's role or display name */
export interface UpdateMemberInput {
  /** The circle */
  circle_id: string
  /** The membership */
  member_id: string
  body: UpdateMemberInputBody
}
export type UpdateMemberResult = MemberResponse

/** Change a raid target */
export interface UpdateRaidTargetInput {
  /** The raid target */
  target_id: string
  body: UpdateRaidTargetInputBody
}
export type UpdateRaidTargetResult = TargetResponse

/**
 * api is every operation the console may call, keyed by `operationId`.
 *
 * The test that replays the console's request set with a scoped token reads its call sites out of
 * `web/src` by this exact shape — `api.<operationId>(` — so an operation a screen reaches is an
 * operation that gate drives, and one it does not reach cannot be smuggled past by spelling a URL.
 */
export const api = {
  authenticateIdentity: (input: AuthenticateIdentityInput, opts?: CallOptions): Promise<Result<AuthenticateIdentityResult>> =>
    send(OPERATIONS.authenticateIdentity, input, opts),
  bindCircleDiscordChannel: (input: BindCircleDiscordChannelInput, opts?: CallOptions): Promise<Result<BindCircleDiscordChannelResult>> =>
    send(OPERATIONS.bindCircleDiscordChannel, input, opts),
  createAuthorizationURL: (input: CreateAuthorizationURLInput, opts?: CallOptions): Promise<Result<CreateAuthorizationURLResult>> =>
    send(OPERATIONS.createAuthorizationURL, input, opts),
  createCircle: (input: CreateCircleInput, opts?: CallOptions): Promise<Result<CreateCircleResult>> =>
    send(OPERATIONS.createCircle, input, opts),
  createIdentityProvider: (input: CreateIdentityProviderInput, opts?: CallOptions): Promise<Result<CreateIdentityProviderResult>> =>
    send(OPERATIONS.createIdentityProvider, input, opts),
  createInvite: (input: CreateInviteInput, opts?: CallOptions): Promise<Result<CreateInviteResult>> =>
    send(OPERATIONS.createInvite, input, opts),
  createRaidTarget: (input: CreateRaidTargetInput, opts?: CallOptions): Promise<Result<CreateRaidTargetResult>> =>
    send(OPERATIONS.createRaidTarget, input, opts),
  createServiceMember: (input: CreateServiceMemberInput, opts?: CallOptions): Promise<Result<CreateServiceMemberResult>> =>
    send(OPERATIONS.createServiceMember, input, opts),
  createTodReport: (input: CreateTodReportInput, opts?: CallOptions): Promise<Result<CreateTodReportResult>> =>
    send(OPERATIONS.createTodReport, input, opts),
  deleteCircle: (input: DeleteCircleInput, opts?: CallOptions): Promise<Result<DeleteCircleResult>> =>
    send(OPERATIONS.deleteCircle, input, opts),
  deleteCircleTimerOverride: (input: DeleteCircleTimerOverrideInput, opts?: CallOptions): Promise<Result<DeleteCircleTimerOverrideResult>> =>
    send(OPERATIONS.deleteCircleTimerOverride, input, opts),
  deleteIdentityProvider: (input: DeleteIdentityProviderInput, opts?: CallOptions): Promise<Result<DeleteIdentityProviderResult>> =>
    send(OPERATIONS.deleteIdentityProvider, input, opts),
  getCircle: (input: GetCircleInput, opts?: CallOptions): Promise<Result<GetCircleResult>> =>
    send(OPERATIONS.getCircle, input, opts),
  getCircleDiscordChannel: (input: GetCircleDiscordChannelInput, opts?: CallOptions): Promise<Result<GetCircleDiscordChannelResult>> =>
    send(OPERATIONS.getCircleDiscordChannel, input, opts),
  getCurrentPrincipal: (input: GetCurrentPrincipalInput, opts?: CallOptions): Promise<Result<GetCurrentPrincipalResult>> =>
    send(OPERATIONS.getCurrentPrincipal, input, opts),
  getInstanceSettings: (input: GetInstanceSettingsInput, opts?: CallOptions): Promise<Result<GetInstanceSettingsResult>> =>
    send(OPERATIONS.getInstanceSettings, input, opts),
  getMember: (input: GetMemberInput, opts?: CallOptions): Promise<Result<GetMemberResult>> =>
    send(OPERATIONS.getMember, input, opts),
  getRaidTarget: (input: GetRaidTargetInput, opts?: CallOptions): Promise<Result<GetRaidTargetResult>> =>
    send(OPERATIONS.getRaidTarget, input, opts),
  getServerMeta: (input: GetServerMetaInput, opts?: CallOptions): Promise<Result<GetServerMetaResult>> =>
    send(OPERATIONS.getServerMeta, input, opts),
  getSetupState: (input: GetSetupStateInput, opts?: CallOptions): Promise<Result<GetSetupStateResult>> =>
    send(OPERATIONS.getSetupState, input, opts),
  getTargetState: (input: GetTargetStateInput, opts?: CallOptions): Promise<Result<GetTargetStateResult>> =>
    send(OPERATIONS.getTargetState, input, opts),
  getTodReport: (input: GetTodReportInput, opts?: CallOptions): Promise<Result<GetTodReportResult>> =>
    send(OPERATIONS.getTodReport, input, opts),
  handleDiscordInteraction: (input: HandleDiscordInteractionInput, opts?: CallOptions): Promise<Result<HandleDiscordInteractionResult>> =>
    send(OPERATIONS.handleDiscordInteraction, input, opts),
  listAdminIdentityProviders: (input: ListAdminIdentityProvidersInput, opts?: CallOptions): Promise<Result<ListAdminIdentityProvidersResult>> =>
    send(OPERATIONS.listAdminIdentityProviders, input, opts),
  listCircleAudit: (input: ListCircleAuditInput, opts?: CallOptions): Promise<Result<ListCircleAuditResult>> =>
    send(OPERATIONS.listCircleAudit, input, opts),
  listCircleDiscordChannels: (input: ListCircleDiscordChannelsInput, opts?: CallOptions): Promise<Result<ListCircleDiscordChannelsResult>> =>
    send(OPERATIONS.listCircleDiscordChannels, input, opts),
  listCircles: (input: ListCirclesInput, opts?: CallOptions): Promise<Result<ListCirclesResult>> =>
    send(OPERATIONS.listCircles, input, opts),
  listCircleTimerOverrides: (input: ListCircleTimerOverridesInput, opts?: CallOptions): Promise<Result<ListCircleTimerOverridesResult>> =>
    send(OPERATIONS.listCircleTimerOverrides, input, opts),
  listIdentityProviders: (input: ListIdentityProvidersInput, opts?: CallOptions): Promise<Result<ListIdentityProvidersResult>> =>
    send(OPERATIONS.listIdentityProviders, input, opts),
  listInvites: (input: ListInvitesInput, opts?: CallOptions): Promise<Result<ListInvitesResult>> =>
    send(OPERATIONS.listInvites, input, opts),
  listMembers: (input: ListMembersInput, opts?: CallOptions): Promise<Result<ListMembersResult>> =>
    send(OPERATIONS.listMembers, input, opts),
  listMyTokens: (input: ListMyTokensInput, opts?: CallOptions): Promise<Result<ListMyTokensResult>> =>
    send(OPERATIONS.listMyTokens, input, opts),
  listQuakes: (input: ListQuakesInput, opts?: CallOptions): Promise<Result<ListQuakesResult>> =>
    send(OPERATIONS.listQuakes, input, opts),
  listRaidTargets: (input: ListRaidTargetsInput, opts?: CallOptions): Promise<Result<ListRaidTargetsResult>> =>
    send(OPERATIONS.listRaidTargets, input, opts),
  listTargetStates: (input: ListTargetStatesInput, opts?: CallOptions): Promise<Result<ListTargetStatesResult>> =>
    send(OPERATIONS.listTargetStates, input, opts),
  listTodReports: (input: ListTodReportsInput, opts?: CallOptions): Promise<Result<ListTodReportsResult>> =>
    send(OPERATIONS.listTodReports, input, opts),
  previewInvite: (input: PreviewInviteInput, opts?: CallOptions): Promise<Result<PreviewInviteResult>> =>
    send(OPERATIONS.previewInvite, input, opts),
  putCircleTimerOverride: (input: PutCircleTimerOverrideInput, opts?: CallOptions): Promise<Result<PutCircleTimerOverrideResult>> =>
    send(OPERATIONS.putCircleTimerOverride, input, opts),
  putRaidTargetTimer: (input: PutRaidTargetTimerInput, opts?: CallOptions): Promise<Result<PutRaidTargetTimerResult>> =>
    send(OPERATIONS.putRaidTargetTimer, input, opts),
  redeemInvite: (input: RedeemInviteInput, opts?: CallOptions): Promise<Result<RedeemInviteResult>> =>
    send(OPERATIONS.redeemInvite, input, opts),
  reinstateMember: (input: ReinstateMemberInput, opts?: CallOptions): Promise<Result<ReinstateMemberResult>> =>
    send(OPERATIONS.reinstateMember, input, opts),
  reportQuake: (input: ReportQuakeInput, opts?: CallOptions): Promise<Result<ReportQuakeResult>> =>
    send(OPERATIONS.reportQuake, input, opts),
  resolveRaidTarget: (input: ResolveRaidTargetInput, opts?: CallOptions): Promise<Result<ResolveRaidTargetResult>> =>
    send(OPERATIONS.resolveRaidTarget, input, opts),
  retractTodReport: (input: RetractTodReportInput, opts?: CallOptions): Promise<Result<RetractTodReportResult>> =>
    send(OPERATIONS.retractTodReport, input, opts),
  revokeInvite: (input: RevokeInviteInput, opts?: CallOptions): Promise<Result<RevokeInviteResult>> =>
    send(OPERATIONS.revokeInvite, input, opts),
  revokeMember: (input: RevokeMemberInput, opts?: CallOptions): Promise<Result<RevokeMemberResult>> =>
    send(OPERATIONS.revokeMember, input, opts),
  revokeToken: (input: RevokeTokenInput, opts?: CallOptions): Promise<Result<RevokeTokenResult>> =>
    send(OPERATIONS.revokeToken, input, opts),
  runSetup: (input: RunSetupInput, opts?: CallOptions): Promise<Result<RunSetupResult>> =>
    send(OPERATIONS.runSetup, input, opts),
  setCircleProviders: (input: SetCircleProvidersInput, opts?: CallOptions): Promise<Result<SetCircleProvidersResult>> =>
    send(OPERATIONS.setCircleProviders, input, opts),
  signOut: (input: SignOutInput, opts?: CallOptions): Promise<Result<SignOutResult>> =>
    send(OPERATIONS.signOut, input, opts),
  stepUpSession: (input: StepUpSessionInput, opts?: CallOptions): Promise<Result<StepUpSessionResult>> =>
    send(OPERATIONS.stepUpSession, input, opts),
  unbindCircleDiscordChannel: (input: UnbindCircleDiscordChannelInput, opts?: CallOptions): Promise<Result<UnbindCircleDiscordChannelResult>> =>
    send(OPERATIONS.unbindCircleDiscordChannel, input, opts),
  updateCircle: (input: UpdateCircleInput, opts?: CallOptions): Promise<Result<UpdateCircleResult>> =>
    send(OPERATIONS.updateCircle, input, opts),
  updateIdentityProvider: (input: UpdateIdentityProviderInput, opts?: CallOptions): Promise<Result<UpdateIdentityProviderResult>> =>
    send(OPERATIONS.updateIdentityProvider, input, opts),
  updateInstanceSettings: (input: UpdateInstanceSettingsInput, opts?: CallOptions): Promise<Result<UpdateInstanceSettingsResult>> =>
    send(OPERATIONS.updateInstanceSettings, input, opts),
  updateMember: (input: UpdateMemberInput, opts?: CallOptions): Promise<Result<UpdateMemberResult>> =>
    send(OPERATIONS.updateMember, input, opts),
  updateRaidTarget: (input: UpdateRaidTargetInput, opts?: CallOptions): Promise<Result<UpdateRaidTargetResult>> =>
    send(OPERATIONS.updateRaidTarget, input, opts),
} as const
