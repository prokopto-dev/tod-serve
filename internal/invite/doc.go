// Package invite mints, resolves and redeems the codes that admit somebody to a circle.
//
// # The code is the capability
//
// A code is instance-unique, so `POST /join` needs no circle id and a person joins by pasting one
// string. It is looked up by `code_hash` on a unique index and NEVER by prefix: a prefix lookup is
// a brute-force oracle, and `invite.code_prefix` exists only so an officer can tell two rows apart
// in a list.
//
// This package owns the hashing, and that ownership is why [HashCode] is handed to
// `identitysql.New` rather than defined there. internal/identity never hashes a code — its port
// takes the code and the hash — precisely so that there is one spelling of the hash in the process.
// Two spellings would let the OAuth flow resolve one invite and redemption resolve another, or
// none, and the failure would look like an expired invite rather than like a bug.
//
// # Codes arrive typed by a human
//
// [Parse] accepts what people actually send: lowercase, spaces, no `TODI-` prefix, no separators,
// and the Crockford substitutions for characters that look alike. It refuses `U`, which Crockford
// excludes rather than substitutes. The canonical form is what is hashed, so every accepted
// spelling of one code resolves to one row.
//
// # The owner grant is not an invite
//
// `invite` carries `CHECK (role <> 'owner')`, so an invite cannot grant ownership and a leaked one
// cannot seize a circle. A circle with no owner still needs a first one, and that is [Grant] — a
// single-use, expiring, hashed code minted by the CLI on the operator's own terminal and consumed
// by one conditional UPDATE. It resolves through the same [Service.Resolve] as an invite, so
// `previewInvite` and `/join` have one code path, and it is a different row in a different table
// because it is a different thing.
package invite
