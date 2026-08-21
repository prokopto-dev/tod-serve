// A gate nobody has seen fail is a gate nobody knows works.
//
// This drives the rule with the shapes it exists to catch, including the two that a naive
// implementation gets wrong in opposite directions: a property called `fetch` on somebody else's
// object is NOT the global, and an aliased local binding is not either.
import { RuleTester } from 'eslint'
import test from 'node:test'

import { noNetworkOutsideApi } from './no-network-outside-api.js'

const tester = new RuleTester({
  languageOptions: { ecmaVersion: 2023, sourceType: 'module' },
})

test('tod/no-network-outside-api', () => {
  tester.run('no-network-outside-api', noNetworkOutsideApi, {
    valid: [
      // The console's own client. Every screen goes through this.
      { code: "import { api } from '../api'\napi.listTargetStates({ circle_id: 'x' })" },
      // A property that happens to be spelled `fetch` is not the global.
      { code: 'const cache = { fetch(key) { return key } }\ncache.fetch(1)' },
      // A local binding shadows the global.
      { code: 'function run(fetch) { return fetch }' },
      // Reading a header off a Response object is not issuing a request.
      { code: "const etag = response.headers.get('ETag')" },
    ],
    invalid: [
      {
        code: "fetch('/api/v1/meta')",
        errors: [{ messageId: 'banned' }],
      },
      {
        code: "const r = await globalThis.fetch('/api/v1/meta')",
        errors: [{ messageId: 'banned' }],
      },
      {
        code: 'const xhr = new XMLHttpRequest()',
        errors: [{ messageId: 'banned' }],
      },
      {
        code: "navigator.sendBeacon('/api/v1/meta', '{}')",
        errors: [{ messageId: 'banned' }],
      },
      {
        code: "const stream = new EventSource('/api/v1/circles/x/events')",
        errors: [{ messageId: 'banned' }],
      },
      {
        code: "const socket = new WebSocket('wss://example.com')",
        errors: [{ messageId: 'banned' }],
      },
      // The shape somebody reaches for when the direct call is refused.
      {
        code: 'const f = fetch\nf("/api/v1/meta")',
        errors: [{ messageId: 'banned' }],
      },
    ],
  })
})
