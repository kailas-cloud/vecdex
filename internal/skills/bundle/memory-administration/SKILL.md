---
name: memory-administration
description: Inspect Vecdex collections, schemas, capabilities and operational limits. Use to configure or diagnose the memory service.
---

# Administer memory

Use `memory_status` for supported operations, dependency health, embedding usage and limits. Use `collection_list` and `collection_get` to inspect available collections and schema before constructing filters. Follow opaque cursors until absent; a short or empty page may still have continuation.

The `read` role can read memory and manage its own contexts. `write` adds document changes. `admin` adds collection creation and deletion. Every tool and resource enforces these rights, even if hidden from discovery. A key rotation with the same principal retains owned contexts. Credentials never belong in document IDs, context handles or logs.

Creating or deleting a collection is a lifecycle operation. Treat `creating` or `deleting` as pending, then inspect the state. An `active` generation is immutable. Conditional deletion must name the expected generation. A collection recreated under the same name is a different collection; create a new context rather than reuse stale handles.

Use `collection_create` with the schema needed by the task, and inspect the result. Use `collection_delete` only within the requested scope, with the expected generation. Diagnose embedding provider and budget errors separately from storage commit failures. A failed provider call is not successful memory storage. Rate limiting is not implemented; do not describe it as enforced.

Streamable HTTP is served at `/mcp`. Modern clients use `2026-07-28`; `2025-11-25` has the base tool, resource and prompt workflow without Skills or subscriptions. Durable contexts do not depend on transport sessions. Subscription notifications are coalesced invalidations, not an event log: after reconnect, reread current resources. Search indexing can lag source commits.

Batch ingestion and scheduled maintenance belong to the embedded Go SDK. No REST, history snapshots, cross-collection search, guaranteed event replay or automatic memory observers are provided. Use paged tools for large results rather than raising budgets without need.
