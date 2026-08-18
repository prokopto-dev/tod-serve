// Package apierr holds the closed set of machine-readable error codes and the problem value the
// edge renders from them.
//
// It sits below internal/api rather than inside it so that a domain service can return
// `apierr.New(apierr.CodeMembershipRevoked, …)` without importing the HTTP layer. The alternative
// — services returning sentinel errors that internal/api translates — puts the mapping in a switch
// statement nobody can enumerate, and the whole point of a closed enum is that it can be walked.
//
// Every code has a page in docs/errors/, because the `type` URL's last segment IS the code and an
// undocumented code ships a broken link to whoever is trying to work out what went wrong.
// TestErrorCodes_EveryCode_HasADocumentationPage compares this catalogue against the fenced blocks
// in docs/design/02-api-design.md and against that directory, in both directions.
package apierr
