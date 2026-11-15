# Investigator: compare causes and request evidence

## Problem and intended behavior

The six repair tools work on bounded cases, but the old input admits one cause category. A slow query may have several plausible explanations. The investigator needs to compare evidence, request a missing measurement, and decline unsupported changes. Jared explicitly requested this implementation on November 14: “build that investigator next so Claude can compare causes and request more evidence. Follow the design docs, test it with real Claude”.

Map nodes: `diagnose-causes` and `request-evidence` under `investigator`, connected to `inspection` under `tools`. Existing Go + TypeScript/LangGraph, one investigator, six repairs and independent Go action guards remain unchanged (d-language r2, d-workflow r1, d-reuse r1).

## Implementation decisions

Use a separate diagnostic graph with visible screen, compare, request and finish nodes. Keep the existing qualified single-call path intact while qualifying the new loop. Go supplies named, typed readers; the model never supplies SQL, credentials, object names or a different target. Each request is bound to the same run, table and column. Existing valid measurements can be supplied together, allowing the one-call fast path. Several cause observations may coexist.

A structured model turn either requests up to three named evidence tools, proposes one repair, or declines. It includes short evidence-based assessments of plausible alternatives, not private chain-of-thought. The tools are plan, indexes, analyze, statistics, vacuum, memory and cron. Missing/denied evidence is explicitly unavailable, never a zero or a negative result. The request router supports caller-provided Go readers; readers remain responsible for their own project grants and bounded database queries. The existing fixture evidence can be reused without repeating database work.

Initial local implementation limits: three model calls, six requested measurements, no repeated requests, 30-second evidence freshness and 22-second whole Go process deadline. These are engineering limits for this qualification, not universal production defaults. Expiry, budget exhaustion, malformed output or tool failure cannot produce a repair. Every emitted proposal must cite available fresh plan/index evidence and any selected repair-specific evidence. Go independently checks the selected proposal. Existing fixture executors retain their exact-family and permission checks; this feature does not authorize arbitrary customer repairs.

Alternative considered: collect all possible measurements before every call. It simplifies orchestration but adds unnecessary database work and model input. The selected loop reuses sufficient evidence and asks only for a stated decision gap. Additional model calls cost time, so evaluate one-call and missing-evidence cases separately.

## Qualification and honest limits

Unit tests cover mixed causes, fresh-evidence reuse, missing evidence, identity changes, invalid tool names, repeated requests, budgets, expiry, malformed model output and interrupted/resumed graph execution. Go bridge tests exercise actual subprocess requests and callback routing. Real-Claude evaluation uses retained Rookery measurements with their provenance preserved as replay, plus ambiguous/healthy/unsupported controls. A real local PostgreSQL reader test must prove that requested evidence can come from a bounded query, not only preloaded objects. Record all attempts, model/tool/total times and outcomes.

This builds and qualifies the investigator mechanism. Replaying Rookery measurements is not a new Rookery repair trial or proof of general customer collectors. The existing staged repair tests retain their own evidence. New customer enrollment, broad workload collectors, all repair variants, dashboard and production operation remain separate work.

## Implementation and evidence

See Tasks 27–29 in implementation.md and progress.md. Results will be appended after execution. LangGraph loop reference: https://docs.langchain.com/oss/javascript/langgraph/thinking-in-langgraph .
