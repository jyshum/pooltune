# Pool-sizing diagnostic review

Fresh independent GPT-6 Astra reviewer `pool_sizing_review` inspected the benchmark
and relevant existing controller/traffic behavior. No implementation agents or
paid calls. This is a benchmark review, not certification of the unfinished
physical adapter or the narrowed background-only product.

No Critical findings. One Important: the first run lost raw observations when a
trial validation called Fatal before serialization. Its automatic trial finished,
but fixed_warm failed a raw server-count assertion, leaving an empty artifact.
The remaining comparators did not run. That run establishes no comparison result.
Its log and exact source context are retained.

`TestEvidenceKeepsFailedTrial` observed RED with empty output, then GREEN after
checkpointing each trial, including explicit validation errors and Complete=false.
The harness now includes observation time and counts in cap errors, writes them
before failing, and only marks the report Complete after every case validates.
No re-review; corrections are verified by tests and the complete rerun.

The count assertion also treated every SHOW SERVERS row as a reusable SQL backend.
The official PgBouncer usage reference identifies active_cancel as cancellation
transport, with new/unidentified connections separate from established backends:
[PgBouncer usage reference](https://www.pgbouncer.org/usage).
`TestPoolInventorySeparatesCancellationConnections` observed RED with the raw-row
counter and GREEN after separating cancellation sockets and new connections from
unique positive backend PIDs. A real cancellation regression then observed 21 raw
rows,20 established pool backend PIDs and 20 application backends in PostgreSQL.
It retains the configured-cap check, rejects unknown inventory/duplicate PIDs,
and records cancellation/connecting counts separately. The original failed run's
specific excess cannot be reconstructed because its offending rows were lost.

Deferred Minor findings:
- Inventory timestamps precede synchronous control/probe work. Treat the samples
  as approximate 1 Hz observations; no exact transition duration or integrated
  connection-saving claim.
- Generic Failed outcomes lack safe SQLSTATE/operation-stage categories. Keep
  failures prominent; do not claim their cause without a separate diagnosis.

Rulings on review exclusions: actual physical-adapter correctness, independent
recovery, tuner crash behavior and hosted compatibility remain separate gates.
The complete exploratory comparison can describe outcomes and observed inventory,
but cannot establish statistical significance, production benefits, monetary
savings or AI usefulness. Fixed_same_path freezes elective control only; report
its observed admission/safety behavior rather than assuming those stayed 20.
Exact source manifests and deltas preserve the dirty measured tree, including
uncommitted physical work. A scoped benchmark commit is not a clean release of
that whole tree. The accepted rerun and post-fix checks are recorded in the result
report; the reviewer did not run load or independently validate that later data.
