package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/prokopto-dev/tod-serve/internal/api"
	"github.com/prokopto-dev/tod-serve/internal/discord"
)

// The verbs this file registers.
const (
	verbDiscord  = "discord"
	verbCommands = "commands"
	verbEndpoint = "endpoint"
)

// newDiscordCommand groups the two things an operator needs to set the bot up, neither of which
// this binary can do for them.
//
// **Registering slash commands is an outbound HTTPS request, and AGENTS.md law 6 confines outbound
// HTTP to `internal/identity` through one guarded client.** A call from here would need a `NET001`
// exception for a request made once per deployment, so instead the binary PRINTS the body and the
// operator sends it. That is also why no bot token is configured on this instance at all: the only
// thing it would have been for is that request.
//
// The endpoint URL is the same shape of answer. It is derived from the route registry rather than
// spelled here, so the string an operator pastes into Discord and the path this binary serves
// cannot drift apart — the same reason `tod-serve doctor` derives the OAuth callback.
func newDiscordCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   verbDiscord,
		Short: "What to give Discord to set the bot up. This binary sends nothing",
		Long: "Two facts Discord needs and this binary cannot deliver itself.\n\n" +
			"`commands` prints the body for PUT /applications/{application.id}/commands, which\n" +
			"you send once with the bot token. This server never makes that request: outbound\n" +
			"HTTP is confined to the identity providers through one guarded client (AGENTS.md\n" +
			"law 6), which is why the bot token is not configuration on this instance.\n\n" +
			"`endpoint` prints the Interactions Endpoint URL, derived from the route registry.\n\n" +
			"See docs/operations/discord-bot.md.\n",
	}
	group.AddCommand(newDiscordCommandsCommand(), newDiscordEndpointCommand())
	return group
}

func newDiscordCommandsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   verbCommands,
		Short: "Print the slash-command registration body to PUT to Discord",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := discord.CommandRegistrationJSON()
			if err != nil {
				return err
			}
			// Straight to stdout with no commentary, so it pipes into curl. The explanation is in
			// `--help` and in the runbook, where it does not end up inside a JSON body.
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
			if err != nil {
				return fmt.Errorf("write the command registration: %w", err)
			}
			return nil
		},
	}
}

func newDiscordEndpointCommand() *cobra.Command {
	return &cobra.Command{
		Use:   verbEndpoint,
		Short: "Print the Interactions Endpoint URL to paste into the developer portal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			public := os.Getenv(envPublicURL)
			if public == "" {
				// Resolved from the database when the variable is unset, exactly as `serve` does:
				// a guessed origin is a URL Discord will POST to and nothing will answer.
				path, err := databasePath(cmd)
				if err != nil {
					return err
				}
				db, closeDB, err := openStore(cmd.Context(), path, textLogger(io.Discard))
				if err != nil {
					return err
				}
				defer closeDB()
				if public, err = publicURL(cmd.Context(), db); err != nil {
					return err
				}
			}
			url, err := api.InteractionsURL(public)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), url)
			if err != nil {
				return fmt.Errorf("write the interactions URL: %w", err)
			}
			return nil
		},
	}
}
