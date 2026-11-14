# How Pooltune improves a database

Pooltune is designed to spot expensive database work, figure out what can improve it, make a permitted change, and check that the change helped. A small team should be able to connect its Supabase project and see what happened without keeping the dashboard open.

## The problem

As an app grows, a database query that used to be cheap can become expensive. It may look through more rows, guess wrong about how many results it will find, or compete with work running at the same time. A slow query is a reason to look closer. The cause determines what to do.

An **index** gives a database a quicker way to find rows. Think of a book index that points you to the right pages. PostgreSQL can use an index to find a few matching records without reading the whole table. It pays for that shortcut with storage and extra work when indexed values change. If a query needs most of the table, reading it in order can be the better choice.

## How a repair works

1. **Measure.** Go collects evidence from PostgreSQL and looks for repeated slowdowns or unusually expensive work. It can compare a query against its own history or a target set for that query.
2. **Investigate.** A workflow in LangGraph gives Claude the chance to inspect a measured query plan, which shows how PostgreSQL ran the query, and check what indexes already exist. The investigator can propose a specific index or close the case. A good investigation connects the proposal to a cause, such as reading thousands of rows to find one match.
3. **Decide and act.** Go checks whether the proposal matches the evidence, whether the owner has granted permission, and whether there is room to run it. If the checks pass, Go asks PostgreSQL to create the index and records what it intended to do.
4. **Verify.** Go compares the same work before and after. It checks that PostgreSQL used the new index, useful reads got faster, and protected operations stayed within limits. If a connection drops mid-build, Go checks what PostgreSQL actually did before trying anything else.

The first repair is an index. The same process will support five other repairs, including refreshing the planner's statistics, helping it estimate rows more accurately, cleaning up old row versions, adjusting memory for a specific function, and moving an approved scheduled job.

## Why these pieces fit

| Piece | Job | Tradeoff |
| --- | --- | --- |
| **Go** | Runs background work and controls what can change. We can test the rules by giving them the same input and checking the outcome. | Rules are predictable and cheap to run. Working out all the possible causes of a slow query this way takes lots of cases. |
| **TypeScript and LangGraph** | TypeScript gives the tools and recommendations a defined shape. LangGraph organizes the investigation into steps, such as screening evidence, investigating, and closing the case. | The workflow is easier to inspect and can use Claude where evidence needs interpretation. A second runtime adds another service to operate, and model calls add time and variability. |
| **PostgreSQL** | The customer's database does the actual work. Pooltune uses a separate database to remember measurements, jobs, and actions. | Recording intent takes extra writes. It gives the worker a way to check for changes that completed just before a crash. |
| **Studio and the dashboard** | Studio lets developers watch the investigator's steps. The customer dashboard will show connected projects, measured outcomes, and a pause control. | Studio helps build and debug. The dashboard needs its own simpler presentation and account permissions. |

## The main tests

- **A healthy lookup.** Pooltune should leave an already fast query alone.
- **A missing index.** A slow, selective lookup should prompt an investigation. Once Go approves the repair, the new index should handle the lookup and reduce the work PostgreSQL does.
- **A misleading signal.** A query that needs many rows, a stale observation, or a suggestion aimed at the wrong table should lead to no change.
- **The side effects.** Measure writes and a healthy query alongside the repaired query. A win on one query can carry a cost elsewhere.
- **An interrupted build.** Start recovery with a fresh connection. Check for an absent, incomplete, or finished index and preserve anything Pooltune cannot identify as its own.

## Where we are

The first automatic index repair ran on disposable test data in Rookery, a real Supabase project. In the live test, Claude inspected the database evidence and recommended the index. Go approved and applied the repair. The mean lookup time went from about **16 ms to 0.026 ms**, and PostgreSQL went from examining **100,000 rows to one** per lookup. The test fixture was removed afterward. This is evidence that the repair works for those test inputs. The deployed service, dashboard, production permissions, and the other repairs are still ahead.

The background worker now observes synthetic application-like reads from a separate connection. It keeps healthy queries quiet and saves an investigation for sustained breaches. The next step is to connect those jobs to LangGraph. [Worker traffic test](worker-traffic.md)

[Engineering notes and exact measurements](index-slice.md) · [Implementation plan](implementation.md)
