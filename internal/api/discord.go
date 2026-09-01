package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/prokopto-dev/tod-serve/internal/apierr"
	"github.com/prokopto-dev/tod-serve/internal/core"
	"github.com/prokopto-dev/tod-serve/internal/discord"
)

// The two headers Discord signs an interaction with.
//
// `X-Signature-Ed25519` is hex over `X-Signature-Timestamp` concatenated with the RAW body, which
// is why the verification reads the buffered bytes rather than a re-marshalled payload: member
// order and whitespace are part of what was signed.
const (
	DiscordSignatureHeader = "X-Signature-Ed25519"
	DiscordTimestampHeader = "X-Signature-Timestamp"
)

// DiscordChannelBinding is one channel bound to one circle, as the API renders it.
//
// It is a distinct name from `discord.Binding` because a type that reaches the OpenAPI document
// needs one that is unique across the whole repository: the schema namer strips the package, and
// two `Binding`s would collide at startup.
type DiscordChannelBinding struct {
	DiscordChannelID string            `json:"discord_channel_id" doc:"The Discord channel. One channel resolves to at most one circle"`
	DiscordGuildID   string            `json:"discord_guild_id" doc:"The guild the binding was made in. An interaction from another guild does not resolve"`
	CircleID         core.CircleID     `json:"circle_id"`
	AllowVisible     bool              `json:"allow_visible" doc:"Whether a reply here may be posted where the channel can read it. Discord has no channel-membership API, so this server cannot know who that is"`
	CreatedBy        core.MembershipID `json:"created_by_membership_id"`
	CreatedAt        core.Micros       `json:"created_at"`
	UpdatedAt        core.Micros       `json:"updated_at"`
}

func bindingView(b discord.Binding) DiscordChannelBinding {
	return DiscordChannelBinding{
		DiscordChannelID: b.ChannelID,
		DiscordGuildID:   b.GuildID,
		CircleID:         b.CircleID,
		AllowVisible:     b.AllowVisible,
		CreatedBy:        b.CreatedBy,
		CreatedAt:        b.CreatedAt,
		UpdatedAt:        b.UpdatedAt,
	}
}

// DiscordChannelBindingResponse is one binding and the instant it was read.
type DiscordChannelBindingResponse struct {
	DiscordChannelBinding
	AsOf core.Micros `json:"as_of"`
}

type listCircleDiscordChannelsInput struct {
	CircleID string `path:"circle_id" doc:"The circle"`
}

type listCircleDiscordChannelsOutput struct{ Body Page[DiscordChannelBinding] }

type bindCircleDiscordChannelInput struct {
	CircleID         string `path:"circle_id" doc:"The circle"`
	DiscordChannelID string `path:"discord_channel_id" doc:"The Discord channel id, 1 to 20 digits"`
	IfMatch          string `header:"If-Match" doc:"The ETag a previous read returned, or * to create a binding that does not exist yet"`
	Body             struct {
		DiscordGuildID string `json:"discord_guild_id" doc:"The guild the channel is in. An interaction whose guild is not this one does not resolve"`
		AllowVisible   bool   `json:"allow_visible,omitempty" doc:"Permit replies the whole channel can read. Defaults to false, and that default is in the DDL: Discord has no channel-membership API, so nobody can enumerate who would see one"`
	}
}

type bindCircleDiscordChannelOutput struct {
	ETag string `header:"ETag"`
	Body DiscordChannelBindingResponse
}

type unbindCircleDiscordChannelInput struct {
	CircleID         string `path:"circle_id" doc:"The circle"`
	DiscordChannelID string `path:"discord_channel_id" doc:"The Discord channel id"`
}

type unbindCircleDiscordChannelOutput struct {
	Body struct {
		DiscordChannelID string      `json:"discord_channel_id"`
		AsOf             core.Micros `json:"as_of"`
	}
}

// discordInteractionInput is the interaction as it arrives.
//
// **The body is `RawBody []byte` rather than a parsed struct, and that is the signature's
// requirement rather than a shortcut.** Discord signs the bytes it sent; a payload decoded by the
// framework and re-marshalled for verification has different member order and whitespace, so a
// verifier fed one refuses every genuine interaction — a failure that looks like a wrong key and is
// found in production. The handler reads the same buffered bytes through [bodyFrom].
//
// It is why the document describes the request as `application/octet-stream`. Discord sends JSON,
// and the summary and [04-identity §9] say so; what this operation CONSUMES is an opaque byte
// string whose exact bytes are load-bearing, and a JSON schema here would describe something this
// handler does not read.
//
// [04-identity §9]: https://github.com/prokopto-dev/tod-serve/blob/main/docs/design/04-identity-and-revocation.md
type discordInteractionInput struct {
	Signature string `header:"X-Signature-Ed25519" doc:"Hex Ed25519 signature over the timestamp and the raw body"`
	Timestamp string `header:"X-Signature-Timestamp" doc:"Seconds since the epoch, as Discord sent them"`
	RawBody   []byte `doc:"Discord's interaction payload, verbatim"`
}

type discordInteractionOutput struct{ Body discord.InteractionReply }

// registerDiscord attaches the Discord operations: the three an officer reaches, and the one
// Discord itself POSTs to.
//
// The interactions route is registered LAST here so the file reads in the order the feature is
// set up: an officer binds a channel, and only then does a command in it resolve to anything.
func (s *Server) registerDiscord() error {
	return errors.Join(
		registerFailure(OpListCircleDiscordChannels, Register(s.api, OpListCircleDiscordChannels,
			func(ctx context.Context, in *listCircleDiscordChannelsInput) (
				*listCircleDiscordChannelsOutput, error,
			) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				rows, err := s.cfg.DiscordBindings.List(ctx, id)
				if err != nil {
					return nil, err
				}
				views := make([]DiscordChannelBinding, 0, len(rows))
				for _, row := range rows {
					views = append(views, bindingView(row))
				}
				return &listCircleDiscordChannelsOutput{
					Body: NewPage(views, "", false, s.cfg.Clock.Now()),
				}, nil
			})),

		registerFailure(OpBindCircleDiscordChannel, Register(s.api, OpBindCircleDiscordChannel,
			func(ctx context.Context, in *bindCircleDiscordChannelInput) (
				*bindCircleDiscordChannelOutput, error,
			) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				if err := s.requireBindingIfMatch(ctx, in.IfMatch, id, in.DiscordChannelID); err != nil {
					return nil, err
				}
				binding, err := s.cfg.DiscordBindings.Bind(ctx, discord.BindRequest{
					CircleID:     id,
					ChannelID:    in.DiscordChannelID,
					GuildID:      strings.TrimSpace(in.Body.DiscordGuildID),
					AllowVisible: in.Body.AllowVisible,
					By:           p.MembershipID,
				})
				if err != nil {
					return nil, err
				}
				view := bindingView(binding)
				etag, err := ETagOf(view)
				if err != nil {
					return nil, apierr.Wrap(apierr.CodeInternalError, err, "")
				}
				return &bindCircleDiscordChannelOutput{ETag: etag, Body: DiscordChannelBindingResponse{
					DiscordChannelBinding: view, AsOf: s.cfg.Clock.Now(),
				}}, nil
			})),

		registerFailure(OpUnbindCircleDiscordChannel, Register(s.api, OpUnbindCircleDiscordChannel,
			func(ctx context.Context, in *unbindCircleDiscordChannelInput) (
				*unbindCircleDiscordChannelOutput, error,
			) {
				id, err := parseCircleID(in.CircleID)
				if err != nil {
					return nil, err
				}
				p, ok := PrincipalFrom(ctx)
				if !ok {
					return nil, apierr.New(apierr.CodeUnauthenticated, "no principal on the request")
				}
				if err := s.cfg.DiscordBindings.Unbind(
					ctx, id, in.DiscordChannelID, p.MembershipID); err != nil {
					return nil, err
				}
				out := &unbindCircleDiscordChannelOutput{}
				out.Body.DiscordChannelID = in.DiscordChannelID
				out.Body.AsOf = s.cfg.Clock.Now()
				return out, nil
			})),

		registerFailure(OpHandleDiscordInteraction, Register(s.api, OpHandleDiscordInteraction,
			func(ctx context.Context, _ *discordInteractionInput) (
				*discordInteractionOutput, error,
			) {
				// The signature was checked by the middleware, over the raw buffered body, before
				// this ran. It is NOT re-checked here and it is not checkable here: the framework
				// has already decoded the request, and a payload re-marshalled from a struct is
				// not the byte string that was signed.
				body := bodyFrom(ctx)
				interaction, err := discord.ParseInteraction(body)
				if err != nil {
					// Past a valid signature this is Discord sending something this version does
					// not model. It is a 400 rather than a reply, because there is no invoker to
					// show a message to for a payload we could not read.
					return nil, apierr.Wrap(apierr.CodeMalformedRequest, err,
						"the interaction payload could not be read")
				}
				reply, err := s.cfg.DiscordCommands.Dispatch(ctx, interaction)
				if err != nil {
					return nil, err
				}
				return &discordInteractionOutput{Body: reply}, nil
			})),
	)
}

// requireBindingIfMatch enforces the concurrency rule on a binding that may not exist yet.
//
// It is [Server.requireOverrideIfMatch]'s reasoning applied to a second create-or-replace PUT: a
// create has no prior tag, so `If-Match: *` is borrowed as "and it must NOT exist"; a replace has
// one, so nothing but that tag will do. The wildcard is therefore REFUSED on an existing binding,
// which matters more here than it does for a timer: the field being overwritten is whether this
// channel may be posted into visibly, and an officer overwriting an update they have not seen is
// an officer silently reversing somebody's disclosure decision.
func (s *Server) requireBindingIfMatch(
	ctx context.Context, header string, circleID core.CircleID, channelID string,
) error {
	current, err := s.cfg.DiscordBindings.Get(ctx, circleID, channelID)
	if err != nil {
		coded, ok := apierr.From(err)
		if !ok || coded.Code() != apierr.CodeNotFound {
			return err
		}
		if strings.TrimSpace(header) != anyETag {
			return apierr.New(apierr.CodePreconditionRequired,
				"this circle has no binding for that channel yet; send If-Match: * to create one").
				WithField("header.If-Match", "must be * when the binding does not exist")
		}
		return nil
	}
	view := bindingView(current)
	if strings.TrimSpace(header) == anyETag {
		body, marshalErr := json.Marshal(view)
		if marshalErr != nil {
			return apierr.Wrap(apierr.CodeInternalError, marshalErr, "")
		}
		return apierr.New(apierr.CodePreconditionFailed,
			"this channel is already bound to this circle; If-Match: * creates a binding, so "+
				"send the ETag you read instead of overwriting a change you have not seen").
			WithCurrent(body)
	}
	return RequireIfMatch(header, view)
}

// checkDiscordSignature is the whole authentication of the interactions endpoint.
//
// **Every refusal is the same `401`,** and that is two rules at once. Discord's own endpoint
// validation POSTs a deliberately-invalid signature when an operator saves the URL and refuses to
// accept an endpoint that answers anything else, so `401` is the designed answer rather than an
// error path. And an unverified interaction is an unauthenticated write, so a missing header, a
// malformed one, a wrong key, an edited body, a stale timestamp and an instance with no public key
// configured are one sentence: telling a forger which part was wrong is telling them what to fix.
//
// It reads the RAW buffered body. `withBufferedBody` runs outside the framework and stores the
// bytes as they arrived, which is what Discord signed — re-encoding a decoded payload changes
// member order and whitespace and would refuse every genuine interaction.
func (b *Builder) checkDiscordSignature(ctx huma.Context) error {
	if b.cfg.DiscordVerifier == nil {
		return apierr.New(apierr.CodeUnauthenticated, "the interaction signature is not valid")
	}
	body := bodyFrom(ctx.Context())
	err := b.cfg.DiscordVerifier.Verify(
		ctx.Header(DiscordSignatureHeader), ctx.Header(DiscordTimestampHeader), body)
	if err != nil {
		b.cfg.Log.WarnContext(ctx.Context(), "refused a Discord interaction",
			slog.String("reason", "signature"))
		return apierr.New(apierr.CodeUnauthenticated, "the interaction signature is not valid")
	}
	return nil
}

// InteractionsURL renders the absolute URL an operator pastes into the developer portal's
// **Interactions Endpoint URL** field.
//
// It is DERIVED from the route registry, for the reason [CallbackBaseURL] is: a second copy of a
// path is a way for the string Discord POSTs to and the path this binary serves to differ
// silently. Discord validates the URL when it is saved — it POSTs a signed `PING` and refuses to
// save unless a well-signed `PONG` comes back — so a wrong one here fails loudly at configuration
// time rather than at 2am, which is the opposite of the redirect-URI failure.
//
// `tod-serve discord endpoint` prints it, and docs/operations/discord-bot.md names that command
// rather than writing the path out a third time.
func InteractionsURL(publicURL string) (string, error) {
	route, ok := Lookup(OpHandleDiscordInteraction)
	if !ok {
		return "", fmt.Errorf("no %s route in the registry", OpHandleDiscordInteraction)
	}
	u, err := originOf(publicURL)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + route.FullPath()
	return u.String(), nil
}
