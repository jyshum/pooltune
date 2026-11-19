# Customer walkthrough, November 15, 2026

## Current checkpoint

Build `6c4cb2269252b0cfd6dc48c6a8cfa935abd21955` is deployed on Railway as
`22a116a2-be8c-42f9-a3e0-5f3d55f301b5` (observed SUCCESS).
Managed setup and six repair families are implemented. The recorded automated
checks passed: Go unit tests, database race/integration tests, investigator type
checking and 118 investigator tests. The focused memory-decline regression and
fresh unit suite cover the last change after the broad integration run.

Earlier live managed evidence proves statistics refresh, exact index rollback
after protected-traffic regression, and a plain cleanup outcome reported as a
regression. It does not establish live success for every new managed path.

## Agreed next work

Jared requested: "okay push the commit and update the map, then walk me through
connecting Rookery like a real customer. let's fix whatever actually breaks there
and then work on the landing page and dashboard design."
He additionally authorized: "we also need to merge to main".

Existing design-conflict mode `ask` and routine review mode `final_only` continue.
This changes the next validation activity, not the approved permissions or repair
safety checks. Broader repair qualification stays open but is not a prerequisite
to beginning this customer walkthrough. Do not claim all map nodes are verified.

1. Push the saved build and corrected map to revamp, then merge and push main.
2. Jared creates his own account or signs in through the deployed landing page.
3. Connect Rookery using the ordinary project form and repair choices.
4. Observe monitoring and activity from actual app use; fix concrete failures.
5. Refine the existing landing, setup and dashboard design with Jared.

## Actual setup requirements

The live site is https://pooltune-web-preview-staging.up.railway.app/.
Preview signup requires an invite code and a password of at least 15 characters.
The invite is an existing deployment credential; provide it through the private
setup flow, never commit its value here.

The project form asks for a name and Supabase project reference. Rookery's
reference is `osdjqwpzgyzwfaxyufze`. The setup form then asks for:

- A project-scoped Supabase Management API token with Database Write and Logs
  Read (Analytics Read). Reuse the approved permanent Rookery token if available.
- The existing postgres database password when enabling repairs.
- The Supabase root certificate in PEM format.
- Repair permission checkboxes. The latency goal is optional, default 1000 ms.

No customer worker deployment, worker key or manual query IDs are required.
Function-memory and scheduling repairs still require choosing the specific
supported functions/jobs and their limits. A checkbox does not authorize every
function or arbitrary job schedule.

The initial success condition is a connected project with advancing monitoring
timestamps and discovered ordinary app queries. A repair is conditional on a
supported cause, sustained slowdown, permissions and benefit evidence; a healthy
project may correctly produce no repair. Do not lower safety gates to manufacture
a success. Keep existing disposable test traffic separate from customer evidence.

## Remaining verification

The fresh customer journey has not yet been completed by Jared. Retained-token
handling, newer managed statistics/memory/scheduling/cleanup outcomes and a
managed mid-repair restart remain open. State restore and broader workload support
remain separate operational gaps. These limits must stay visible on the map.

## November 19 workspace update

Jared requested a permissions-first setup and a project workspace with separate pages for overview, queries, changes, permissions and connection. The existing invite, account session and Supabase authorization checks still apply.

Each setup tab carries its own account-bound repair choices through database selection. Starting the selected project confirms those choices. Choosing no repairs enables monitoring only. Existing sessions open the project list.

The overview charts use recorded monitor checks, measured calls and investigations over the trailing 24 hours. The changes page follows saved parent runs and can page back through retained investigations.

Pause keeps the connection and its repair choices. Disconnect checks that the connection still matches the one the owner reviewed. Manual undo is available for successful managed index changes that recorded the created index's identity. Older records, maintenance operations and changed indexes do not qualify.

The contact photo and links come from Jared's portfolio. Its published domain is www.jaredshum.ca.
