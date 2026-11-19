# Maintenance, function memory, and scheduled-job repair value

Research checked November 14, 2026. Six sources retained below. Existing Pooltune reports were background, not evidence. No customer database was accessed and no savings were measured.

## Does bounded plain VACUUM solve a real problem beyond native autovacuum?

### Takeaway

Yes, maintenance problems occur in production. Extra manual vacuum is a conditional repair, because PostgreSQL says autovacuum is sufficient for many installations. [PostgreSQL routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)

### Cited Findings

- Standard VACUUM makes dead-row space reusable and updates visibility information for index-only scans. It usually does not return space to the operating system; the exception is empty tail pages. VACUUM FULL rewrites the table and needs an exclusive lock. Vacuum itself creates substantial I/O. [PostgreSQL routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)
- GitLab’s production runbook records both aggressive autovacuum consuming resources during peak traffic and performance harm from bloated tables/indexes, referencing a January 2022 web-performance incident. Its proposal to shift maintenance requires confidence that quiet windows can finish the deferred work. This is first-party operational evidence, not a representative survey or proof of a completed Pooltune-style repair. [GitLab production runbook](https://runbooks.gitlab.com/patroni/postgresql-vacuum/)

### Inferences

- Product value is catching a demonstrated cleanup backlog that native maintenance has not handled, then verifying healthier reads or slower storage growth. A healthy database may need no intervention for the entire week. [PostgreSQL routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)
- Repeated manual cleanup without fixing the maintenance mismatch can add avoidable work. GitLab’s evidence argues for diagnosis, not automatic vacuum whenever a dead-row count rises. [GitLab production runbook](https://runbooks.gitlab.com/patroni/postgresql-vacuum/)

### Gaps

- No weekly startup prevalence or quantified saving from bounded plain VACUUM was established.
- With tail truncation excluded by Pooltune’s operating envelope, table-file shrinking is not the promised result. Do not substitute VACUUM FULL results for this family’s evidence.
- Old transactions or replication state can retain required row versions. This repair does not authorize removing those blockers. Case-specific eligibility still needs inspection.

## How useful is function-scoped work_mem for disk spills?

### Takeaway

The mechanism is established, but coverage is narrow: an enrolled database function must contain the expensive operation. Evidence for spill tuning generally is not evidence that most customer slow queries fit this repair. [PostgreSQL function settings](https://www.postgresql.org/docs/17/sql-createfunction.html), [PostgreSQL resource settings](https://www.postgresql.org/docs/17/runtime-config-resource.html)

### Cited Findings

- work_mem controls memory available to individual sort/hash operations before temporary disk files are used. Several operations and sessions can allocate it concurrently. Hash operations also use hash_mem_multiplier; parallel workers each receive their own limits. [PostgreSQL resource settings](https://www.postgresql.org/docs/17/runtime-config-resource.html)
- A function SET clause applies a configuration value at entry and restores the prior value at exit. An ordinary SET inside the function can override this behavior. [PostgreSQL function settings](https://www.postgresql.org/docs/17/sql-createfunction.html)

### Inferences

- A suitable repair can trade spare memory for less temporary I/O and shorter duration. Qualification needs representative inputs, simultaneous callers, plan operations, and available memory. Removing one spill is not proof that the persistent setting is safe under load. [PostgreSQL resource settings](https://www.postgresql.org/docs/17/runtime-config-resource.html)
- SQL issued directly by an application is outside a function-only repair. A quiet week or an application without relevant database functions can yield zero opportunities. [PostgreSQL function settings](https://www.postgresql.org/docs/17/sql-createfunction.html)

### Gaps

- No first-party customer case establishing the frequency or business value of this exact function-scoped repair was found in the bounded search. Official mechanics establish plausibility, not product demand.
- No measured Pooltune speedup, memory safety result, or financial saving exists in this research. Controlled fixtures can validate mechanics but cannot fill those demand gaps.

## Can moving a known Cron job create legitimate weekly value?

### Takeaway

Yes, overlapping background work can harm application performance. Whether moving it helps depends on an actual spare execution window and the customer’s deadline. [Supabase Cron troubleshooting](https://supabase.com/kb/troubleshooting/pgcron-debugging-guide-n1KTaz), [Chatbase customer account](https://supabase.com/customers/chatbase)

### Cited Findings

- Supabase’s troubleshooting guide says simultaneous pg_cron jobs each use a database connection and recommends spacing jobs to avoid connection overload and failure. It documents job history and supported schedule-editing functions. This is operational guidance, not incidence data. [Supabase Cron troubleshooting](https://supabase.com/kb/troubleshooting/pgcron-debugging-guide-n1KTaz)
- Supabase reports Chatbase’s scheduled analytics routinely saturated disk throughput and reached its IOPS ceiling at least weekly, affecting unrelated transactional queries. The proposed response combines query changes, more disk capacity, and an analytics replica. It does not report a successful cron-rescheduling experiment, establish pg_cron ownership, or quantify cash savings. This is a vendor-published customer story. [Chatbase customer account](https://supabase.com/customers/chatbase)

### Inferences

- One change can protect repeated application peaks throughout a week. Count verified improvement during those peaks separately from the number of schedule edits. Moving work normally changes its timing, not its total resource requirement. [Supabase Cron troubleshooting](https://supabase.com/kb/troubleshooting/pgcron-debugging-guide-n1KTaz)
- Eligibility requires a known enrolled job, grants, flexible timing, preserved frequency/dependencies/deadlines, and evidence of contention. Arbitrary slow queries and externally scheduled reports are not automatically covered. Chatbase proves interference occurs; it does not prove this narrower repair would solve that case. [Chatbase customer account](https://supabase.com/customers/chatbase)

### Gaps

- No representative fraction of startup databases with movable jobs was established. A week with no relevant job, no contention, or no acceptable alternative window legitimately yields no repair.
- Faster queries and fewer incidents are distinct from lower invoices. Report cash savings only after a measured billing reduction or a substantiated avoided capacity purchase. None of these sources demonstrates that outcome for these three exact repairs.
- Product validation still needs observed eligible cases, successful repairs, preserved application behavior, and customer value. Real underlying problems are supported; recurring demand for Pooltune’s precise authorized actions remains unproven.
