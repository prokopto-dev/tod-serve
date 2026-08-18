package core

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/prokopto-dev/tod-serve/internal/schemaenum"
)

// Server is a Project 1999 server. A circle is pinned to exactly one, immutably, and there is no
// combined view anywhere — see ADR-0009.
type Server string

// The servers. The values come from the enum catalogue, so the strings that reach the wire, the
// CHECK constraint and the OpenAPI schema are one string and not three.
const (
	ServerBlue  Server = schemaenum.ServerBlue
	ServerGreen Server = schemaenum.ServerGreen
	ServerRed   Server = schemaenum.ServerRed
)

// ErrInvalidServer is returned for a value outside the catalogue.
var ErrInvalidServer = errors.New("invalid server")

// Servers returns every server, in catalogue order.
//
// TestServers_Catalogue_MatchesSchemaEnum asserts this agrees with internal/schemaenum in both
// directions, which is why this can be a literal rather than a lookup that has to handle a
// not-found case it can never hit.
func Servers() []Server { return []Server{ServerBlue, ServerGreen, ServerRed} }

// Valid reports whether s is a known server.
func (s Server) Valid() bool {
	for _, known := range Servers() {
		if s == known {
			return true
		}
	}
	return false
}

// String returns the wire and database value.
func (s Server) String() string { return string(s) }

// ParseServer validates a server name.
func ParseServer(s string) (Server, error) {
	server := Server(s)
	if !server.Valid() {
		return "", fmt.Errorf("parse server %q: %w", s, ErrInvalidServer)
	}
	return server, nil
}

// UnmarshalJSON validates on the way in. Without this a bad server reaches the CHECK constraint
// and surfaces as a 500 at write time instead of a 422 at the edge.
func (s *Server) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("unmarshal server %s: %w: %w", b, ErrInvalidServer, err)
	}
	parsed, err := ParseServer(raw)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}
