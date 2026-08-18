# `auth_flow_expired`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/auth_flow_expired`

The `auth_flow` behind this callback's `state` is gone — expired, already consumed, or never
issued by this instance.

## What causes it

- The user took too long between being redirected to the provider and coming back.
- The callback was replayed with a `state` that has already been consumed.
- A `state` this instance never issued. **This is also what a CSRF attempt looks like**, and it is
  refused for the same reason it is refused when it is innocent.

## What the client should do

Start again from `createAuthorizationURL`. There is nothing to recover: the flow row holds the
server-side PKCE verifier, and without it the code exchange cannot be completed at all — which is
precisely why the verifier is kept on the server rather than handed to the browser.
