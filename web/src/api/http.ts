// The transport. This is the ONLY file in the console that touches the network.
//
// `web/src` contains no `fetch` and no `XMLHttpRequest` outside `web/src/api` — enforced by the
// `tod/no-network-outside-api` ESLint rule and, separately, by the WEB001 grep in
// scripts/repo-gates.sh, which runs in the CI job that has no npm at all. Two mechanisms rather
// than one because they fail differently: a lint rule is switched off by an `eslint-disable`
// comment, and a grep is not.
//
// The rule is not about tidiness. A request issued from a component is a request nobody can drive
// from a test with a scoped token, which is exactly the capability the nParse+ plugin would then
// never be able to reach.

import type { OperationSpec, Problem } from './generated'

/**
 * Params is one request's inputs, flattened: the path parameters, the query parameters and the
 * body under `body`.
 *
 * It is an index signature rather than `Record<string, unknown>` because the generated module
 * carries a schema NAMED `Record` — the audit log's row — and the two must not be confusable.
 */
export type Params = { [key: string]: unknown }

/** CallOptions are the per-request headers a caller may need to set by hand. */
export interface CallOptions {
  /**
   * IfMatch quotes back the entity tag a previous read returned. It is REQUIRED on the operations
   * whose spec says so — they overwrite state a read supplied, and a request without one is
   * refused with 428 rather than silently racing another officer.
   */
  ifMatch?: string
  /** IfNoneMatch revalidates a cached copy. A match answers 304 and no body. */
  ifNoneMatch?: string
  /**
   * IdempotencyKey is the retry key for a state-creating POST.
   *
   * A fresh one is minted per call when the caller does not supply one. Supply one when a RETRY
   * of the same user action has to replay rather than create a second row — the server keys on
   * `(membership, key)`, so it replays across a token rotation too.
   */
  idempotencyKey?: string
  signal?: AbortSignal
}

/** Ok is a response that carried a representation. */
export interface Ok<T> {
  status: number
  data: T
  /** ETag, when the operation returns one. Quote it back in `If-Match` or `If-None-Match`. */
  etag: string | null
  notModified: false
}

/** NotModified is a `304`: the caller's cached copy is still current, and there is no body. */
export interface NotModified {
  status: 304
  data: null
  etag: string | null
  notModified: true
}

export type Result<T> = Ok<T> | NotModified

/**
 * ProblemError is an RFC 9457 failure, thrown rather than returned.
 *
 * The `code` is from a closed enum and is what a caller branches on — never the HTTP status and
 * never the detail string. `step_up_required` and `session_required` are two different fixes and
 * a console that could not tell them apart would tell somebody to log in again when what they
 * actually need is an officer.
 */
export class ProblemError extends Error {
  readonly problem: Problem
  readonly status: number

  constructor(problem: Problem) {
    super(problem.detail || problem.title)
    this.name = 'ProblemError'
    this.problem = problem
    this.status = problem.status
  }

  get code(): Problem['code'] {
    return this.problem.code
  }

  /** fieldErrors renders `errors[]` as `location -> message`, for a form to attach them. */
  fieldErrors(): Record<string, string> {
    const out: Record<string, string> = {}
    for (const e of this.problem.errors ?? []) {
      if (e.location) out[e.location] = e.message ?? ''
    }
    return out
  }
}

/**
 * TransportError is a request that never reached the API: the network is down, the server is
 * unreachable, the response was not JSON.
 *
 * It is deliberately NOT a ProblemError with an invented code. A console that rendered "internal
 * error" for an unplugged laptop would send somebody looking at the server logs.
 */
export class TransportError extends Error {
  constructor(message: string, options?: { cause?: unknown }) {
    super(message, options)
    this.name = 'TransportError'
  }
}

/**
 * body is the representation a call returned, for a caller that did not revalidate.
 *
 * A `304` reaching here is a bug rather than an outcome: the server answers one only to an
 * `If-None-Match`, so a call that sent none and got one has been answered by something in front of
 * the API. Saying so is better than handing a screen a null it will render as an empty table.
 */
export function body<T>(result: Result<T>): T {
  if (result.notModified) {
    throw new TransportError(
      'the server answered 304 to a request that carried no If-None-Match; something in front of ' +
        'the API is caching',
    )
  }
  return result.data
}

/**
 * toError narrows a caught value to an Error.
 *
 * Everything this module throws already is one — [ProblemError], [TransportError], or the
 * browser's own `AbortError` — so this is the boundary where `unknown` stops. Screens then hold
 * `Error | null` rather than `unknown`, which is what lets a failure be rendered without a cast at
 * every call site.
 */
export function toError(value: unknown): Error {
  return value instanceof Error ? value : new Error(String(value))
}

const PROBLEM_MEDIA_TYPE = 'application/problem+json'

/** newIdempotencyKey mints a retry key. */
function newIdempotencyKey(): string {
  return crypto.randomUUID()
}

/** buildURL substitutes the path parameters and appends the query ones. */
function buildURL(spec: OperationSpec, input: Params): string {
  let path = spec.path
  for (const name of spec.pathParams) {
    const value = input[name]
    if (value === undefined || value === null || value === '') {
      throw new TransportError(`${spec.id}: path parameter ${name} is missing`)
    }
    path = path.replace(`{${name}}`, encodeURIComponent(String(value)))
  }
  const query = new URLSearchParams()
  for (const name of spec.queryParams) {
    const value = input[name]
    // An empty string is a real filter value nowhere in this API, and treating it as one would
    // turn a cleared search box into `q=`, which matches everything rather than nothing.
    if (value === undefined || value === null || value === '') continue
    query.set(name, String(value))
  }
  const suffix = query.toString()
  return suffix ? `${path}?${suffix}` : path
}

/**
 * send issues one operation.
 *
 * Everything it knows about the request comes from the generated [OperationSpec] — the method, the
 * path, which parameters are in the path and which in the query, and whether `Idempotency-Key` is
 * required. Nothing here is spelled per call site, so a console screen cannot get a URL wrong and
 * cannot omit a header the server requires.
 */
export async function send<T>(
  spec: OperationSpec,
  // `object` rather than an index-signature type, so the generated per-operation input interfaces
  // pass without a cast at every call site. What is in it is decided by the spec, not by this
  // signature: the path and query parameter names come off [OperationSpec].
  input: object,
  opts: CallOptions = {},
): Promise<Result<T>> {
  const params = input as Params
  const headers = new Headers({ Accept: 'application/json' })
  const body = params.body

  if (body !== undefined) headers.set('Content-Type', 'application/json')
  if (spec.idempotency !== '') {
    headers.set('Idempotency-Key', opts.idempotencyKey ?? newIdempotencyKey())
  }
  if (opts.ifMatch) headers.set('If-Match', opts.ifMatch)
  if (opts.ifNoneMatch) headers.set('If-None-Match', opts.ifNoneMatch)

  let response: Response
  try {
    response = await fetch(buildURL(spec, params), {
      method: spec.method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      // The session cookie is `__Host-`-prefixed and therefore same-origin by construction. The
      // console is served by the same binary as the API, so there is no cross-origin case to
      // support and `include` would only widen what the browser is willing to attach.
      credentials: 'same-origin',
      signal: opts.signal,
    })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    throw new TransportError(`${spec.id}: the request did not reach the server`, { cause })
  }

  const etag = response.headers.get('ETag')
  if (response.status === 304) {
    return { status: 304, data: null, etag, notModified: true }
  }

  if (!response.ok) {
    throw new ProblemError(await readProblem(response, spec))
  }

  if (response.status === 204) {
    return { status: response.status, data: null as T, etag, notModified: false }
  }
  let data: T
  try {
    data = (await response.json()) as T
  } catch (cause) {
    throw new TransportError(`${spec.id}: the response body was not JSON`, { cause })
  }
  return { status: response.status, data, etag, notModified: false }
}

/**
 * readProblem parses a failure body.
 *
 * A failure that is not `application/problem+json` is a TransportError rather than an invented
 * problem: it means something in front of the API answered — a reverse proxy, a captive portal, a
 * load balancer with its own error page — and telling the user "internal error" would name the
 * wrong machine.
 */
async function readProblem(response: Response, spec: OperationSpec): Promise<Problem> {
  const contentType = response.headers.get('Content-Type') ?? ''
  if (!contentType.startsWith(PROBLEM_MEDIA_TYPE)) {
    throw new TransportError(
      `${spec.id}: the server answered ${response.status} with ${contentType || 'no content type'}, ` +
        'which did not come from this API',
    )
  }
  try {
    return (await response.json()) as Problem
  } catch (cause) {
    throw new TransportError(`${spec.id}: the problem body did not parse`, { cause })
  }
}
