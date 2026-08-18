# Error codes

One page per machine-readable `code`, because the `type` URL in every
[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem response is
`https://docs.tod-serve.org/errors/<code>` and **the last segment IS the code** — an undocumented
code ships a broken link to whoever is trying to work out what went wrong.

**There is deliberately no index of codes on this page.** The list lives in exactly one place, the
fenced block in [02-api-design.md](../design/02-api-design.md#error-codes), and `DOC001` in
`scripts/docs-check.sh` parses that block and fails if any code in it has no page here. A second
hand-maintained list would be a second thing to forget.

Each page says what the code means, what causes it, and what the client should do. Where the honest
answer is "nothing on the client fixes this", the page says that instead of suggesting a retry.
