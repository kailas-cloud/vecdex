---
name: memory-handoff
description: Create or resume a concise Vecdex working context with source references. Use for a chat handoff or agent-authored observations.
---

# Hand off work

Keep one working context per task or chat unless the user needs shared work. Discover existing contexts with `context_list`, inspect one with `context_get`, and resolve pins when resuming. Read current referenced sources before treating the handoff as fact.

Store an observation or summary as an ordinary memory document, then pin its document ID and committed version using `context_update`. The write and the pin update are separate operations. Contexts store references, not hidden text copies. Context updates require their own current version and idempotency key; reconcile conflicts by reading and merging the current pins and settings.

Write a short account of the current task, confirmed outcomes, open questions and the next useful action. Preserve important details in source documents and link them. Do not make an automatic Observer or Reflector, and do not repeatedly compress away details as the only retained evidence.

Optional metadata convention:
- `kind`: for example `observation`, `handoff` or `decision`.
- `statement_type`: `user_statement`, `agent_inference`, `plan` or `confirmed_result`.
- `source_role`: who supplied the statement, not who stored it.
- `observed_at`: when the evidence was seen; `effective_at`: when it applies.
- `source_refs`: source identifiers or links; `thread_id` and `resource_id`: task and subject scope.

Keep assistant advice distinct from a user assertion. A plan whose due date has passed is still a plan until evidence confirms an outcome. Preserve contradictory sources with attribution. If a detail is missing from a summary, read original evidence rather than infer it. Similarity search cannot count all events.

Contexts default to seven days from creation. Read does not renew expiry. Set an explicit expiry or `null` for persistence only when the task calls for it. Deleting a context closes the handoff without deleting its documents.

Example: save “migration proposed; deployment not yet verified” with `statement_type: plan`, pin it, and record the verification step. After deployment evidence arrives, create or update the observation with the evidence and `confirmed_result`, then update the pin version.
