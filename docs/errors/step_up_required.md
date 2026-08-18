# `step_up_required`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/step_up_required`

You hold a browser session with the right permission, and it has not re-authenticated recently
enough for a capability-floor operation.

## What causes it

Floor operations are session **and step-up**: the session must have proved the identity again
within the step-up window. A tab left open all afternoon still authenticates you; it does not prove
that you are the person now typing into it.

`meta.step_up_window_seconds` on the problem response says how fresh the proof has to be.

## What the client should do

Re-authenticate and repeat the request. This is distinct from
[`session_required`](session_required.md), which no amount of re-authentication fixes because the
credential is the wrong *kind*.
