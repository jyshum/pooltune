# Guarded index repair on Rookery from Railway

The existing Go repair command ran from the Railway staging consumer container against a new private Rookery fixture. The TypeScript LangGraph workflow produced a scripted recommendation. Go checked the recommendation against the current table and column, confirmed the fixture matched the durable action record, built the index concurrently, and checked the resulting index. The test used the Rookery lab owner login because PostgreSQL requires ownership for this index build. The temporary connection details were supplied to the one-shot process.

## Before and after

The trial ran 40 target lookups, 40 healthy control lookups, and 40 value-changing updates on each side. Four matched inputs were used. The writes changed the indexed value and ran in a transaction that rolled back. Timings below are PostgreSQL statement execution times.

| Measure | Before | After |
| --- | ---: | ---: |
| Target lookup mean | 10.330 ms | 0.030 ms |
| Rows examined to return one | 100,000 | 1 |
| Shared-page accesses per target lookup | 1,334 | 3 |
| Healthy lookup mean | 0.032 ms | 0.022 ms |
| Value-changing update mean | 0.118 ms | 0.061 ms |
| Measured errors | 0 | 0 |

The plan changed from a table scan to an index scan. The index was valid and ready and used 2,260,992 bytes. The healthy table kept its original index. The guard found no write regression in the sampled inputs. Two write inputs were slower by about 0.001 ms and 0.005 ms. Both changes were below the regression thresholds. The average was lower on this short run. A production workload needs a longer write comparison with concurrency and commit costs.

The build took 1.679 s. The scripted investigation took 0.419 s. The full trial took 16.701 s, including setup, measurements, and cleanup. This timing starts at the repair command and is separate from the detector's rolling windows. Railway's state database recorded `complete` for run `0ad685007ee6e1a7b208f49f` with four graph checkpoints. A separate Rookery connection verified that the trial schema was gone and all 12 public index definitions matched the start of the test.

## Recovery drill

A separate run paused after PostgreSQL returned from the concurrent build and before Go verified it. We sent a kill signal to the one-shot lab process. Its supervising shell returned exit code 137. The active detector and consumer kept their normal staging roles.

| Check | Observed result |
| --- | --- |
| Journal immediately after the stop | Run `7a716fb280dac032a0561acf` was still `building`, with zero post-build reads recorded. |
| PostgreSQL after the stop | The intended 2,260,992-byte index was valid and ready on the matching fixture. |
| Fresh recovery process | Recorded `recovered` and `valid`, then left the fixture absent. Recovery took 4.835 s including the Railway SSH connection. |
| Second recovery pass | The same journal result remained and there were no fixture schemas left. |

Independent catalog checks found the same 12 public indexes before and after both runs. Recovery verified what the interrupted build had left behind and removed the fixture. It did not establish a performance outcome for the interrupted run. The local PostgreSQL integration suite also exercises missing and invalid index cases and preserves a replacement table whose identity differs from the journal.

## Reproduce from staging

Run the installed lab binary from `/app` on Railway staging using the verified Rookery lab connection and the private staging state connection. Pass connection details and the Supabase CA through a private channel. The normal command is `pooltune-query-lab -target rookery -index-trial scripted`.

For a separate recovery drill, confirm no other Rookery trial is active or pending. Start the same command with `-pause-after-index-build`. The flag requires the scripted Rookery route and Railway private state. Stop the exact one-shot child after the ready message and confirm signal exit before reading the pending journal and catalog. Run `pooltune-query-lab -target rookery -recover-index-trials` with the same target identity and state. Repeat recovery to verify it has no more work. The pause expires after 20 seconds if the process is left running.

The race-enabled Go integration suite, Go vet, TypeScript checks, investigator tests, and shell syntax checks passed. Independent code review found no blocking issues.

## Design choices and scope

- **Reuse the existing repair boundary.** The graph recommends a table and column by identity. Go issues the DDL after checking the exact fixture and its action journal. This test moves the guarded repair into the hosted environment. The normal staged detector and consumer still do their existing jobs. Attaching the repair to the queued workflow and qualifying customer privileges are separate work.
- **Measure a write that can incur index maintenance.** A changed value exercises the extra work the new index adds. The rollback keeps the next comparison scoped to the same fixture inputs. The lab does not measure committed or sustained writes.
- **Reconcile before deciding what to do next.** PostgreSQL can finish a build even if the caller stops before it verifies the result. The repair writes its intent before DDL. Recovery checks PostgreSQL with a fresh connection and only removes the identified synthetic fixture. The action journal and graph checkpoints answer different questions. [PostgreSQL concurrent index builds](https://www.postgresql.org/docs/17/sql-createindex.html)
