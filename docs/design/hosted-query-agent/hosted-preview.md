# Account and dashboard preview

This slice turns the existing enrolled-query worker into a product path. An invited user creates a Pooltune account, signs in, creates an owner-scoped project and follows an installation guide. The database owner creates the dedicated monitor login and enrolls exact app-role statements. The dashboard verifies the connection before activating the same detector and read-only Sonnet path previously qualified from the operator command. It displays actual saved activity and can disconnect the worker.

PostgreSQL is the source for account membership, sessions, monitored projects, query windows and job history. Each route checks account ownership. Store monitor credentials and an optional scoped Logs Read token under an authenticated encryption key held outside PostgreSQL. Use HTTPS cookies for the hosted preview and allow local HTTP on loopback. Invite-code registration keeps the preview usable without public enrollment. This first flow uses email and password. Account recovery and email address verification remain later release work.

Use a small server-rendered Go dashboard served beside the account routes. This reuses the application runtime and avoids a separate UI service before there is a need for one. The alternate design is a separate frontend consuming a public API, which is useful for broader client integrations but adds another hosted service and authentication boundary. Reuse the landing page’s charcoal palette and turquoise accent.

The owner installs the same SQL used for Rookery. For an established enrolled identity, the supported reader can use an owner-approved lookup grant or a scoped ordinary-traffic plan grant. Monitoring also works for identity-bound counters without a plan. A useful diagnosis still depends on the evidence supported for that query. The dashboard gives the monitor no database change privilege. The approved V1 repair contracts and the review boundary before customer changes remain in place.

## Connect your Supabase project

1. [Create a preview account](https://pooltune-web-preview-staging.up.railway.app/signup) with the private invite code, then add a project with your Supabase project reference.
2. Run **Installation SQL** from the dashboard in the project's `postgres` database as the database owner. Use the dashboard's `psql` commands to set a fresh password and enable the dedicated monitor login.
3. Run the dashboard's discovery query after your application has exercised the queries you want to watch. Enroll the exact app role and query ID with a mean latency objective in milliseconds. Use the optional lookup grant when the statement qualifies. For measured evidence, use the [owner-captured lookup procedure](enrolled-plans.md) or the [ordinary-traffic plan procedure](traffic-plans.md) when the role and workload meet that source's requirements.
4. Paste the dedicated monitor password and the root certificate PEM from Supabase's database connection settings. Select **Start read-only monitoring**.
5. After your app sends traffic across a full monitoring window, open the project to see query calls, mean latency, block accesses, detection decisions, saved diagnoses and poll times. The **Download history** link exports the saved summaries as JSON. Use **Disconnect** to stop new collection and keep the existing history. Reconnect with the existing monitor credentials and certificate, or reinstall if you removed the role.

The preview checks once a minute and evaluates five-minute windows. It keeps up to eight days of poll and window history within the configured row limits. The worker admits at most eight model calls per project per UTC day, and the preview supports five active projects. A project still collects metrics after reaching its daily diagnosis limit.

For a seven-day pilot, connect the intended project manually and leave it active through ordinary application traffic. Review poll gaps, missing versus inactive query windows, the latency objective and diagnosis for each sustained slowdown, and any diagnosis budget hits. The current preview supports manually installed Supabase monitoring with direct TLS. Broader account recovery, workload coverage and customer repair qualification are tracked on the design map.

## Verified on Railway

The hosted test created an account, signed out and back in, and manually connected a dedicated Rookery fixture using the dashboard's SQL. Each run sent 100 ordinary calls to a slow lookup and 100 to a healthy control. The slow lookup queued one investigation. Sonnet saved a passive diagnosis on the first run and used an owner-captured sequential scan on the second. The measured run kept its diagnosis and resumed polling after reconnect. A final disconnect and readback found zero pilot roles, schemas or sessions and unchanged public indexes. [Raw results and deployment checks](evidence/hosted-preview-railway.json)
