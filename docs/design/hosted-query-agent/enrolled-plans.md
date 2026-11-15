# Live measured plans for enrolled lookups

Task 39 adds one owner-captured PostgreSQL execution sample to the existing enrolled lookup diagnosis. The project owner supplies a representative integer and runs the capture in a short read-only transaction. PostgreSQL executes the fixed lookup under its enrolled app role. The owner publishes the typed summary in a separate transaction. Railway reads the sample alongside normal workload counters and can still diagnose from passive evidence when a current plan is unavailable.

The first slice admits one schema-qualified integer equality lookup that uses FROM ONLY and the built-in equality operator on a small ordinary heap. It locks the exact relation, checks its identity and the enrolled query identifier without executing it, and then runs one measured EXPLAIN. The capture refuses unsupported scan shapes, a different role or identity, an unsafe relation, or an unbounded session. Application rows, the sample value, full SQL, plan predicates and relation names stay out of the published plan. PostgreSQL must confirm the same statement generation and current catalog shape before Pooltune submits the evidence to Sonnet and again before it publishes a diagnosis. The approved plan is distinct from normal workload measurements and cannot enter the repair contract.

The owner-operated sample uses the existing private SQL setup and an approved app role. It avoids project-wide plan logging and putting an application credential into Railway. This test qualifies one app-role sample under the specified session settings. Task 40 adds a [separate opt-in source for ordinary app traffic](traffic-plans.md).

Capture is an owner-operated experiment. Run it while schema changes and maintenance are paused for the enrolled table. Its nonblocking Access Share lock pins the relation identity and does not prevent concurrent index creation or ANALYZE. Pooltune compares the catalog before and after the sample and checks again when it reads the plan. A later detected change withholds the sample but cannot undo the bounded execution. The Railway worker can read a sample and cannot trigger an execution.

## Capture and publish

After installing the existing monitor and evidence SQL, run the output of `pooltune-project setup-plan-sql -ref "$PROJECT_REF" -key "$INSTALLATION_KEY"` as the same database owner. The installer also teaches existing installations to recognize the supported `ONLY` spelling. The app role must already have access to the target and the owner must be allowed to assume that role. Enroll the exact observed bound-parameter query ID and authorize the matching table and column through `allow_lookup`. A normal equality statement with a different query ID can still use passive diagnosis.

Use `psql` as the database owner. The example shows one owner-selected input. Substitute the actual installation schema, app role OID, signed query ID and input before running it.

```sql
BEGIN READ ONLY;
SET LOCAL statement_timeout = '750ms';
SET LOCAL transaction_timeout = '2s';
SELECT pt_monitor_<INSTALLATION_KEY>.capture_lookup_plan(
    12345::oid, -1234567890123456789::bigint, 17
) AS plan_receipt
\gset
COMMIT;

SELECT pt_monitor_<INSTALLATION_KEY>.publish_lookup_plan(:'plan_receipt'::jsonb);
\unset plan_receipt
```

The sample expires in two minutes. Deleting it revokes the plan while leaving passive diagnosis available. Deleting the lookup grant removes the sample. Database checks validate the seven permitted scalar fields on both write and read. A saved diagnosis reference requires the same plan to still be readable after Sonnet responds. Internal state may retain evidence from an already-admitted provider call.

The sample uses the app role's permissions through `SET LOCAL ROLE` with the capture session's settings. It does not reproduce settings applied on app login, request context, prepared-plan cache or parallel execution.

## Railway and Rookery qualification

The private [Enrolled Queries service](https://railway.com/project/a32a2ecd-2d0f-43d0-b473-3d43cff623e0/service/f1f104b6-7c88-462e-8a66-721ba6662190?environmentId=0541335c-4db7-40cf-a75d-761ad418e816) ran source `3162f73` on deployment `f4242c95-a401-417a-8a8f-a6b83f3c09a8`. The owner captured an actual bound-parameter lookup on an owned 100,000-row table in live Rookery. The bounded capture took one execution under the test app role. It reported a sequential scan, one estimated and one actual returned row, 99,999 filtered rows, 443 shared buffer hits, zero shared reads and 35.827 ms of sample execution time. That duration describes the one capture.

The normal traffic driver sent 80 slow and healthy pairs with zero errors. Both top-level app counters rose from 1 to 81, and the one explicit owner capture was tracked separately. Railway queued only the slow lookup as job `155` and saved a diagnosis on attempt one, 24.106 seconds after traffic started. The worker spent 6.584 s on the investigation, including one 4.328 s `claude-sonnet-5-5` call. The saved-job event confirmed the measured sequential scan was attached, with missing index as the supported cause. The Railway worker made no additional sample executions and applied no change. [Recorded evidence](evidence/enrolled-plans-railway.json)

Railway disconnected the pilot before the fixture was removed. The service is running in private standby on deployment `6ed07f5d-39f2-429a-88e7-841631b25a38`. Rookery has zero pilot roles, schemas or sessions. The public index fingerprint was unchanged, the pilot variables were removed, and the three existing Railway deployments kept their previous IDs.

The local PostgreSQL test verifies denied capture by the monitor, rejection of raw plan fields, expiry, index change invalidation, and withholding a result after grant revocation. A first local exactness check rejected a bare FROM lookup because its identifier differed from the bounded FROM ONLY statement. The final scope deliberately uses one exact observed spelling and preserves passive diagnosis for the older shape. Go race tests and vet, affected PostgreSQL integrations, independent source review, and 96 TypeScript tests and type checking passed.
