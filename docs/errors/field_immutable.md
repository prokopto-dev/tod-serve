# `field_immutable`

**HTTP 422** · `type: https://docs.tod-serve.org/errors/field_immutable`

You tried to change a field that cannot change after creation.

## What causes it

- `circle.server`. A circle is pinned to one server permanently — trigger-enforced in the database
  as well as rejected at the edge. A guild raiding Blue and Green runs **two circles**; there is no
  row in the schema where a Blue fact and a Green fact can meet.

## What the client should do

Drop the field from the `PATCH` body. If you genuinely need the other server, create a second
circle: the multi-destination client already holds several `(endpoint, token, circle)` destinations,
so reporting to both is two ticked boxes.
