// The console's only door to the network.
//
// Everything a screen needs is re-exported here so that a component imports from `../api` and
// never from `../api/http` — there is one import path, and the ESLint rule and the WEB001 grep
// both describe one directory.

export { api, OPERATIONS } from './generated'
export type { OperationId, OperationSpec } from './generated'
export { body, ProblemError, TransportError, toError } from './http'
export type { CallOptions, NotModified, Ok, Result } from './http'
export * from './generated'
