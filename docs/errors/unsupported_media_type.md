# `unsupported_media_type`

**HTTP 415** · `type: https://docs.tod-serve.org/errors/unsupported_media_type`

The request body's `Content-Type` is one this operation does not read.

## What causes it

Almost always a missing or wrong `Content-Type` on a request that carries JSON. The server does not
sniff the body: guessing a content type is how a proxy's helpful rewrite becomes a parse error three
layers down.

## What the client should do

Send `Content-Type: application/json`.
