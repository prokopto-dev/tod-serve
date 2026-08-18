# `service_unavailable`

**HTTP 503** · `type: https://docs.tod-serve.org/errors/service_unavailable`

The instance is up and cannot serve this request right now.

## What causes it

A dependency the operation needs is unreachable — most often the database during a migration or a
restore. `/healthz` deliberately does **not** touch the database, so a container mid-migration stays
alive to finish; `/readyz` does, and is what a load balancer should be watching.

An unreachable identity provider is [`identity_provider_unreachable`](identity_provider_unreachable.md)
instead, because that failure is somebody else's and points at a different fix.

## What the client should do

Retry with backoff. Nothing about the request needs changing.
