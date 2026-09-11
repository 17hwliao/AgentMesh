# Plan

1. Define a provider-neutral request/result contract with immutable citations.
2. Implement deterministic fake and explicitly configured Ollama JSON adapter.
3. Add authenticated tenant-route handler, timeout, scope guard, input hash and
   provider-output validation.
4. Mount it separately from chat and embedding; retain SQL Sentinel's existing
   OpenAI-style embedding response (`data[index].embedding`) unchanged.
5. Add offline tests, compose startup guidance, and opt-in real-service gates.
