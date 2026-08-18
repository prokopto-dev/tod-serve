# `not_acceptable`

**HTTP 406** · `type: https://docs.tod-serve.org/errors/not_acceptable`

The `Accept` header asks for a representation this server does not produce.

## What causes it

An `Accept` naming only content types the API does not serve. The API speaks JSON; error responses
are `application/problem+json`, which is JSON.

## What the client should do

Send `Accept: application/json`, or omit the header — an absent `Accept` gets JSON.
