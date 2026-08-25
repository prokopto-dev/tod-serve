/**
 * `tod/no-network-outside-api` — the console reaches the network only from `web/src/api`.
 *
 * AGENTS.md law 7. The rule is not about tidiness: a request issued straight from a component is
 * a request with no operationId, no `Idempotency-Key`, no `If-Match` and no entry in the published
 * document — so the API-parity test cannot replay it with a scoped token, and whatever it does is
 * a capability the nParse+ plugin can never reach. That is the exact failure mode this project is
 * built to refuse.
 *
 * It is a lint rule AND a grep (WEB001 in scripts/repo-gates.sh) because the two fail differently.
 * A lint rule is silenced by an `eslint-disable` comment on the offending line; the grep is not,
 * and it runs in the CI job that has no npm installed at all.
 *
 * What it catches, and why each one:
 *
 *   - `fetch(...)` and `globalThis.fetch` — the obvious one.
 *   - `new XMLHttpRequest()` — the one somebody reaches for when the lint rule stops the first.
 *   - `navigator.sendBeacon(...)` — a POST that does not look like one.
 *   - `new EventSource(...)` and `new WebSocket(...)` — realtime is Phase 6 and lands in the API
 *     directory with everything else, not in whichever component wanted it first.
 *   - `import(...)` of a URL is NOT caught, and that is stated rather than silently true: the
 *     bundler resolves module specifiers at build time and there are none at runtime here.
 */

/** The global functions that issue a request. */
const BANNED_GLOBALS = new Map([
  ['fetch', 'fetch'],
  ['XMLHttpRequest', 'new XMLHttpRequest'],
  ['EventSource', 'new EventSource'],
  ['WebSocket', 'new WebSocket'],
])

/** The member expressions that issue one without naming a banned global on their own. */
const BANNED_MEMBERS = new Map([
  ['navigator.sendBeacon', 'navigator.sendBeacon'],
  ['globalThis.fetch', 'globalThis.fetch'],
  ['window.fetch', 'window.fetch'],
  ['self.fetch', 'self.fetch'],
  ['globalThis.XMLHttpRequest', 'globalThis.XMLHttpRequest'],
  ['window.XMLHttpRequest', 'window.XMLHttpRequest'],
])

/** memberPath renders `a.b` for a simple, non-computed member expression, or null. */
function memberPath(node) {
  if (node.type !== 'MemberExpression' || node.computed) return null
  if (node.object.type !== 'Identifier' || node.property.type !== 'Identifier') return null
  return `${node.object.name}.${node.property.name}`
}

/** @type {import('eslint').Rule.RuleModule} */
export const noNetworkOutsideApi = {
  meta: {
    type: 'problem',
    docs: {
      description:
        'The console reaches the network only from web/src/api, so every request it makes is an ' +
        'operation the published document carries.',
    },
    schema: [],
    messages: {
      banned:
        '{{name}} is not permitted here. The console reaches the network only from web/src/api: ' +
        'a request issued outside it carries no operationId, so the API-parity test cannot replay ' +
        'it with a scoped token and the capability becomes browser-only. Add the call to ' +
        'web/src/api instead — see AGENTS.md law 7.',
    },
  },

  create(context) {
    const report = (node, name) => context.report({ node, messageId: 'banned', data: { name } })

    return {
      // Resolved through the SCOPE rather than by matching the identifier text.
      //
      // That is what makes the two opposite mistakes both come out right: a parameter or import
      // named `fetch` shadows the global and is not the network, and a global reached through an
      // alias — `const f = fetch` — still is. A textual rule gets exactly one of those right.
      'Program:exit'(program) {
        const scope = context.sourceCode.getScope(program)
        for (const [name, label] of BANNED_GLOBALS) {
          const variable = scope.set.get(name)
          const references = variable
            ? variable.references
            : scope.through.filter((r) => r.identifier.name === name)
          for (const reference of references) {
            report(reference.identifier, label)
          }
        }
      },

      MemberExpression(node) {
        const path = memberPath(node)
        if (path && BANNED_MEMBERS.has(path)) report(node, BANNED_MEMBERS.get(path))
      },
    }
  },
}

export default { rules: { 'no-network-outside-api': noNetworkOutsideApi } }
