---
level: L1
feature: 024-rag-grounded-answer
created: 2026-09-11
---

# Grounded RAG Answer Boundary

## Goal

Expose authenticated `POST /v1/rag/answers` for a future RAG query service to
pass already-retrieved project knowledge or CandidateSpec chunks to a
constrained answer provider.

## Required behaviour

- The API key supplies tenant identity; request bodies cannot choose a tenant.
- `model` is resolved through the existing tenant route. `mock` is the offline
  fake route, and `ollama` is an opt-in upstream route.
- Input contexts require `source_uri`, `document_id`, `version`, `chunk_id`,
  `hash`, and content whose SHA-256 hash equals `hash`.
- Every affirmative fact contains at least one full citation exactly matching an
  input chunk. Provider prose has no separate uncited response field.
- With no evidence, return only `refusal_code=insufficient_evidence`.
- Requests for SQL, DDL, query plans, indexes, performance advice/certification
  or policy overrides return `refusal_code=out_of_scope` before provider use.
- Retrieval content is isolated as quoted untrusted data in the Ollama system
  prompt; it cannot alter the system instruction.
- Responses expose only `trace_id`, provider, logical model and bounded usage
  counts/duration. They never expose API keys, endpoint values, raw upstream
  errors, or prompts.

## Non-goals

This feature does not retrieve documents, run SQL, produce DDL, make a
performance decision, validate CandidateSpec correctness, or claim a real LLM
has been reached. The caller owns retrieval and must preserve tenant isolation
before invoking this endpoint.

## Acceptance

1. HTTP contracts cover auth, tenant/model routing, tampered hash rejection,
   SQL refusal, mandatory citations, and provider error mapping.
2. `AGENTMESH_RAG_ANSWER_PROVIDER=ollama` requires endpoint/model and a
   1–300 second timeout before networking; fake is offline by default.
3. Real Ollama is marked verified only when the explicit verification command
   returns success. Cloud LLMs are not implemented or verified in this slice.
