# `retract_not_permitted`

**HTTP 403** · `type: https://docs.tod-serve.org/errors/retract_not_permitted`

You may not retract this report.

## What causes it

- You hold `tod.retract`, which covers **your own** reports, and this one is somebody else's.
  Retracting another member's report needs `tod.retract.any`.
- Your token's scopes do not include `tod:retract`. Effective capability is `role permissions ∩
  token scopes`, so a narrow token cannot exceed its role — and a broad role cannot rescue a narrow
  token.

## What the client should do

Ask an officer to retract it, or use a token with `tod:retract`. Note this is a within-circle
permission failure, which is why it is `403` and not `404`: wrong tenant is `404`, right tenant and
insufficient permission is `403`.
