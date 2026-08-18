# `report_immutable`

**HTTP 409** · `type: https://docs.tod-serve.org/errors/report_immutable`

You tried to modify a report. **The report log is append-only** — `tod_report` is never `UPDATE`d
or `DELETE`d, by database trigger, in Go, in SQL, or in a migration.

## What causes it

- A `PATCH` or `PUT` against a report. No such operation exists.
- An attempt to correct a report in place.

## What the client should do

**Corrections are new rows.** Retract the report (`retractTodReport`, which writes a *new*
retraction row and leaves the original visible) and post a fresh one. This is not a restriction to
be routed around: the log is the audit trail, and the whole trust argument for deriving state rather
than storing it collapses if the evidence can be edited.
