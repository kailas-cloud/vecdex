---
name: memory-maintenance
description: Correct, deduplicate, expire or remove Vecdex memory while preserving concurrent edits. Use for memory cleanup and repair.
---

# Maintain memory

Use an owned context for the intended collection. Read the current document and version before changing it. Search or `memory_similar` can suggest duplicates, but read each candidate before deciding it is redundant.

Use `memory_update` with `if_version` and a new idempotency key. Content replaces the whole source text. Metadata uses JSON Merge Patch: omitted properties stay, `null` removes a property, arrays replace, and a top-level `null` clears metadata. Omitted expiry stays; explicit `null` makes the source persistent. Confirm the returned committed version by direct read.

On version conflict, read the current source, reconcile the actual concurrent change, and make a new conditional request with a new key. Never bypass the precondition to force a stale edit. On an uncertain transport result, retry the identical request with the same key; receipts last 24 hours. A replay may describe an earlier commit after the document has been changed or deleted; it does not resurrect it or prove it is still current.

Use `memory_delete` with the read version and an idempotency key. Context pins are references: removing a pin does not remove a source, and deleting a source does not update pins. Update the context separately when the task requires it. TTL expiry is logical deletion for reads and subscriptions; reads do not extend TTL.

A `stale` pin means the source changed; a `missing` pin means it cannot be read. A version is a condition on current state, not a way to retrieve history. If a source was replaced, ask for or find independent evidence for old details instead of reconstructing them from a compressed note.

Example: read two alleged duplicates, retain the more useful source, copy only verified missing details with a conditional update, then conditionally delete the redundant source if the task authorizes that removal. Report conflicts or unresolved evidence explicitly.
