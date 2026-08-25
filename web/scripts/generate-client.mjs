// Generates web/src/api/generated.ts from the checked-in openapi/openapi.json.
//
// The document is the contract, so the client is derived from it rather than written beside it.
// Two things follow, and both are the point:
//
//   - There is no hand-written request type anywhere in web/src. A field the server renamed is a
//     TypeScript error in the console, not a runtime `undefined` somebody notices on a raid night.
//   - Every operation the console can reach is an operation the document publishes, under the
//     `operationId` an SDK generator would use. There is no way to spell a URL here, so there is
//     no way to grow a private back door — which is what the API-parity test asserts from the
//     other end.
//
// `--check` regenerates into memory and exits non-zero if the checked-in file differs. That is the
// gate: a spec change that nobody regenerated is a red build rather than a client that silently
// disagrees with the server.
import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..', '..')
const SPEC = join(root, 'openapi', 'openapi.json')
const OUT = join(root, 'web', 'src', 'api', 'generated.ts')

const spec = JSON.parse(readFileSync(SPEC, 'utf8'))

// The extensions the route registry writes into every operation. They are READ rather than
// re-derived: what reaches the console is what the middleware itself enforces.
const EXT_PERMISSION = 'x-tod-permission'
const EXT_SCOPES = 'x-tod-scopes'
const EXT_SESSION_ONLY = 'x-tod-session-only'
const EXT_CIRCLE_SCOPED = 'x-tod-circle-scoped'
const EXT_IDEMPOTENCY = 'x-tod-idempotency'
const EXT_IF_MATCH = 'x-tod-if-match-required'
const EXT_ETAG = 'x-tod-etag'

/** pascal renders an operationId as a type-name prefix. */
const pascal = (s) => s.charAt(0).toUpperCase() + s.slice(1)

/** quote renders a string as a TypeScript literal. */
const quote = (s) => `'${String(s).replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`

/** ident renders a property name, quoting the ones that are not bare identifiers. */
const ident = (name) => (/^[A-Za-z_$][A-Za-z0-9_$]*$/.test(name) ? name : quote(name))

/** doc renders a description as a single-line JSDoc comment, or nothing. */
const doc = (text, indent) =>
  text ? `${indent}/** ${String(text).replace(/\s+/g, ' ').replace(/\*\//g, '*\\/')} */\n` : ''

/**
 * tsType renders one JSON Schema node as a TypeScript type.
 *
 * It handles exactly the shapes this document contains. An unrecognised node becomes `unknown`
 * rather than `any`: `any` would let a field the server never sends flow silently into a
 * component, which is the failure this generator exists to prevent.
 */
function tsType(schema, indent = '  ') {
  if (!schema || typeof schema !== 'object') return 'unknown'
  if (schema.$ref) return schema.$ref.replace('#/components/schemas/', '')
  if (schema.allOf) return schema.allOf.map((s) => tsType(s, indent)).join(' & ')
  if (schema.oneOf) return schema.oneOf.map((s) => tsType(s, indent)).join(' | ')

  const types = Array.isArray(schema.type) ? schema.type : [schema.type]
  const nullable = types.includes('null')
  const base = types.filter((t) => t && t !== 'null')
  const render = (t) => {
    switch (t) {
      case 'string':
        return schema.enum ? schema.enum.map(quote).join(' | ') : 'string'
      case 'integer':
      case 'number':
        return 'number'
      case 'boolean':
        return 'boolean'
      case 'array':
        return `Array<${tsType(schema.items, indent)}>`
      case 'object':
        return objectType(schema, indent)
      default:
        return 'unknown'
    }
  }
  const rendered = base.length ? base.map(render).join(' | ') : 'unknown'
  return nullable ? `${rendered} | null` : rendered
}

function objectType(schema, indent) {
  const props = schema.properties || {}
  const names = Object.keys(props).sort()
  if (!names.length) {
    // TypeScript's `Record<K, V>` is deliberately NOT used anywhere in this file. The document
    // carries a schema called `Record` — the audit log's row — and emitting `export type Record`
    // shadows the built-in utility type for the whole module, so every later use of it resolves
    // to an audit row and the file does not compile. An index signature says the same thing and
    // cannot collide with a schema name.
    if (schema.additionalProperties && typeof schema.additionalProperties === 'object') {
      return `{ [key: string]: ${tsType(schema.additionalProperties, indent)} }`
    }
    return '{ [key: string]: unknown }'
  }
  const required = new Set(schema.required || [])
  const inner = indent + '  '
  const lines = names.map((name) => {
    const child = props[name]
    const q = required.has(name) ? '' : '?'
    return `${doc(child.description, inner)}${inner}${ident(name)}${q}: ${tsType(child, inner)}`
  })
  return `{\n${lines.join('\n')}\n${indent}}`
}

// --- the operation table ------------------------------------------------------------------------

function successSchema(op) {
  for (const status of Object.keys(op.responses || {})) {
    if (!/^2\d\d$/.test(status)) continue
    const schema = op.responses[status].content?.['application/json']?.schema
    if (schema) return schema
  }
  return null
}

const operations = []
for (const [path, item] of Object.entries(spec.paths)) {
  for (const method of ['get', 'post', 'put', 'patch', 'delete']) {
    const op = item[method]
    if (!op) continue
    if (!op.operationId) throw new Error(`${method.toUpperCase()} ${path} carries no operationId`)
    const params = op.parameters || []
    operations.push({
      id: op.operationId,
      method: method.toUpperCase(),
      path,
      summary: op.summary || '',
      pathParams: params.filter((p) => p.in === 'path'),
      queryParams: params.filter((p) => p.in === 'query'),
      body: op.requestBody?.content?.['application/json']?.schema,
      result: successSchema(op),
      permission: op[EXT_PERMISSION] || { kind: 'unknown', requires_step_up: false },
      scopes: op[EXT_SCOPES] || [],
      sessionOnly: Boolean(op[EXT_SESSION_ONLY]),
      circleScoped: Boolean(op[EXT_CIRCLE_SCOPED]),
      idempotency: op[EXT_IDEMPOTENCY] || '',
      ifMatch: Boolean(op[EXT_IF_MATCH]),
      etag: Boolean(op[EXT_ETAG]),
    })
  }
}
operations.sort((a, b) => a.id.localeCompare(b.id))

// --- rendering ------------------------------------------------------------------------------------

const out = []
out.push('// Code generated from openapi/openapi.json by web/scripts/generate-client.mjs.')
out.push('// DO NOT EDIT. Run `make gen-web` from the repository root.')
out.push('//')
out.push('// Every request the console makes goes through one of the functions at the bottom of this')
out.push('// file, and every one of them names an `operationId` the published document carries. There')
out.push('// is no way to spell a URL here, which is what stops the console growing a capability the')
out.push('// nParse+ plugin can never reach.')
out.push('')
out.push("import { send, type CallOptions, type Result } from './http'")
out.push('')
out.push('/** RFC 3339 with microsecond precision, always UTC. Never read against the browser clock. */')
out.push('export type Timestamp = string')
out.push('')
out.push('/** EmptyInput is an operation that takes no path parameter, no query parameter and no body. */')
out.push('export type EmptyInput = { readonly [key: string]: never }')
out.push('')


for (const name of Object.keys(spec.components.schemas).sort()) {
  const schema = spec.components.schemas[name]
  out.push(`${doc(schema.description, '')}export type ${name} = ${tsType(schema, '')}`)
  out.push('')
}

out.push('/** OperationId is every operation the published document carries. */')
out.push(`export type OperationId =\n${operations.map((o) => `  | ${quote(o.id)}`).join('\n')}`)
out.push('')
out.push(`/**
 * OperationSpec is what the route registry declares about an operation, carried through the
 * document so the console reads the same facts the middleware enforces.
 *
 * \`sessionOnly\` and \`stepUp\` are here so the console can SAY it is stepping up rather than
 * letting a 403 arrive with nothing to explain it.
 */
export interface OperationSpec {
  readonly id: OperationId
  readonly method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  readonly path: string
  readonly pathParams: readonly string[]
  readonly queryParams: readonly string[]
  /** The PAT scopes that reach it. Empty means no token does, at any scope. */
  readonly scopes: readonly string[]
  /** No personal access token reaches this operation; a browser session is the only credential. */
  readonly sessionOnly: boolean
  /** The capability floor: session only, and recently re-authenticated. */
  readonly stepUp: boolean
  readonly circleScoped: boolean
  /** Non-empty when \`Idempotency-Key\` is required. */
  readonly idempotency: '' | 'membership' | 'handler'
  /** The operation returns an entity tag. */
  readonly etag: boolean
  /** \`If-Match\` is required: the operation overwrites state a previous read supplied. */
  readonly ifMatch: boolean
}`)
out.push('')
out.push('export const OPERATIONS = {')
for (const o of operations) {
  out.push(`  ${o.id}: {`)
  out.push(`    id: ${quote(o.id)},`)
  out.push(`    method: ${quote(o.method)},`)
  out.push(`    path: ${quote(o.path)},`)
  out.push(`    pathParams: [${o.pathParams.map((p) => quote(p.name)).join(', ')}],`)
  out.push(`    queryParams: [${o.queryParams.map((p) => quote(p.name)).join(', ')}],`)
  out.push(`    scopes: [${o.scopes.map(quote).join(', ')}],`)
  out.push(`    sessionOnly: ${o.sessionOnly},`)
  out.push(`    stepUp: ${Boolean(o.permission.requires_step_up)},`)
  out.push(`    circleScoped: ${o.circleScoped},`)
  out.push(`    idempotency: ${quote(o.idempotency)},`)
  out.push(`    etag: ${o.etag},`)
  out.push(`    ifMatch: ${o.ifMatch},`)
  out.push('  },')
}
// `satisfies` rather than a type annotation, so each entry keeps its literal type while still
// being checked against [OperationSpec]. It is spelled with an index signature rather than
// TypeScript's `Record<K, V>` for the reason objectType() states: the document carries a schema
// NAMED `Record`, and it shadows the built-in for this whole module.
out.push('} as const satisfies { [K in OperationId]: OperationSpec }')
out.push('')

for (const o of operations) {
  const P = pascal(o.id)
  const fields = []
  for (const p of o.pathParams) {
    fields.push(`${doc(p.description, '  ')}  ${ident(p.name)}: ${tsType(p.schema, '  ')}`)
  }
  for (const p of o.queryParams) {
    fields.push(`${doc(p.description, '  ')}  ${ident(p.name)}?: ${tsType(p.schema, '  ')}`)
  }
  if (o.body) fields.push(`  body: ${tsType(o.body, '  ')}`)
  out.push(doc(o.summary, '') + (fields.length
    ? `export interface ${P}Input {\n${fields.join('\n')}\n}`
    : `export type ${P}Input = EmptyInput`))
  out.push(`export type ${P}Result = ${o.result ? tsType(o.result, '') : 'null'}`)
  out.push('')
}

out.push(`/**
 * api is every operation the console may call, keyed by \`operationId\`.
 *
 * The test that replays the console's request set with a scoped token reads its call sites out of
 * \`web/src\` by this exact shape — \`api.<operationId>(\` — so an operation a screen reaches is an
 * operation that gate drives, and one it does not reach cannot be smuggled past by spelling a URL.
 */
export const api = {`)
for (const o of operations) {
  const P = pascal(o.id)
  out.push(`  ${o.id}: (input: ${P}Input, opts?: CallOptions): Promise<Result<${P}Result>> =>`)
  out.push(`    send(OPERATIONS.${o.id}, input, opts),`)
}
out.push('} as const')
out.push('')

const rendered = out.join('\n')

if (process.argv.includes('--check')) {
  let current = ''
  try {
    current = readFileSync(OUT, 'utf8')
  } catch {
    console.error(`${OUT} does not exist; run \`make gen-web\``)
    process.exit(1)
  }
  if (current !== rendered) {
    console.error(
      'web/src/api/generated.ts is stale: openapi/openapi.json has moved under it.\n' +
        'Run `make gen-web` and commit the result.',
    )
    process.exit(1)
  }
  console.log(`generated.ts matches openapi/openapi.json (${operations.length} operations)`)
} else {
  writeFileSync(OUT, rendered)
  console.log(`wrote web/src/api/generated.ts (${operations.length} operations)`)
}
