# Managed AI gateway

The gateway runs on Pooltune-owned infrastructure with its own SQLite database.
Customer traffic does not call a model. Customer control sends bounded, approved
aggregates through an installation token. The provider credential belongs only in
the hosted gateway's secret file or secret manager, outside source control.

Owner access uses `pooltune-admin` locally on that infrastructure. Approve an
account with an explicit lifetime allowance and expiry, writing its random token
to a new private file. The database stores only its SHA-256 hash. Rotate preserves
all spending; revoke blocks future dispatches. Copies share one account's limits.
There is no public account approval endpoint. Token files require a private 0700
parent and are created 0600. Do not paste credentials into chat or commit them.

Amounts are integer millionths of a US dollar. Before every model step, a SQLite
transaction reserves the maximum input/output cost permitted by the configured
profile. Settled usage replaces that reservation. Timeouts, missing usage,
accounting failures and ambiguous responses retain the full reservation, including
its concurrency slot. An owner must reconcile those against provider evidence;
there is no automatic timeout refund. Restart and token rotation cannot reset the
lifetime cap. Changing persisted accounting limits is rejected.

The gateway issues account-bound run IDs, allows at most eight ordered steps per
run, and enforces expiry, rate limits, account/global concurrency and account/global
budgets. Repeated steps are rejected without replay. TLS and server-to-server
requests are required; browser origins and arbitrary model IDs are rejected.
Bodies and model responses are bounded. No redirects or automatic inference retries
are enabled. HTTP server resource limits are also required when wiring the service.

Claude receives fixed system instructions and client-tool schemas. The local
controller still validates every tool call against current guardrails and versions.
Descriptions need explicit consent. Raw SQL, rows, credentials and request bodies
must be excluded by local typed context construction. Gateway structural checks
supplement this minimization; they cannot establish that arbitrary customer text
is truthful or contains no sensitive data.

A model profile fixes model ID, pricing, input/output ceilings, payload size and
timeout. Token-count preflight includes instructions/tools and requires 1,024
spare tokens below the input ceiling. This allowance depends on the approved
provider profile; it is not a mathematical guarantee against a provider billing
change. Cached usage, unsupported output or usage above the profile fails closed
and retains the reservation. No web/search/server-side paid tools are configured.
Verify pricing and tool compatibility before enabling an actual profile.

## Verification status

Local tests cover concurrent separate database connections, restart retention,
account/global spending, token copies, rotation/revocation, duplicate steps,
eight-step exhaustion, missing/oversized token estimates, malformed/private
payloads, arbitrary model overrides, unknown costs and accounting outage. The
Claude HTTP fixture proves request sequencing and parsing only. It is not evidence
of a real model run, current pricing accuracy or hosted deployment security.

Paid integration testing has now run with the replacement private secret file and
verified Haiku 5.5 profile. The development budget remains $5 TOTAL across runs,
enforced in one persistent gateway database with a 5,000,000 micro-dollar cap.
No posted conversation credential was used or saved. See evidence/model-run.md
for both attempts, exact local accounting and the limits of the successful result.
G2 hosted deployment and full G5 product evidence remain open.
