# Private owner control

React/TypeScript assets are served by the Go control process. The management
handler is separate from public REST and PostgreSQL ingress. HTTP development
access requires a literal loopback origin and loopback client. Other profiles
require actual TLS; forwarded headers cannot declare a request secure. Host and
origin checks protect the private surface from unexpected browser origins.

A private one-time claim token establishes the single administrator password.
The claim hash and expiry persist in control SQLite and claiming is atomic.
Restart does not reopen setup or extend an existing token's expiry. Passwords
use golang.org/x/crypto/bcrypt at cost 12, with 12–72 byte bounds. Login/claim
attempts are bounded to ten per minute and two concurrent hash operations per
process. Do not publish the claim token in URLs or browser storage.

Sessions are random tokens whose hashes live server-side. Cookies are HttpOnly,
SameSite=Strict and Secure outside loopback development. Sessions expire and can
be revoked individually or all together. Mutations require a session-bound CSRF
header, exact origin, JSON content type and an expected revision/idempotency key.
The mutation backend must persist intent and use the shared activation coordinator;
a successful login does not bypass runtime checks. Request bodies are bounded and
credentials are never echoed. Owner-facing backend DTOs must exclude secrets.

Setup covers a preconfigured connection profile, bounded verification, observation,
AI draft if approved access exists, review of known workload priorities and initial
policy approval. Semantic sharing needs an exact separate preview; this UI keeps
it off. Known-workload mappings and advanced queue/capacity bounds still require
server-side validation. Observation does not launch a load sweep. Interrupted setup
resumes from server state, not localStorage. Polling is sequential and stale responses
cannot overwrite a newer request. Expired or revoked sessions return to sign-in.

The dashboard displays limits, queue/latency/error aggregates, experiment status,
revision outcomes and agent runs. Freeze and restore report pending/uncertain
results explicitly. Restore creates a new revision; it cannot undo database writes.

## Verification and release boundary

Unit tests cover atomic claim, persistent claim/session state, expiry rejection,
logout/revoke-all, CSRF/origin restrictions, stale commands and unauthenticated data
access. Real Chromium tests exercise the built UI and real Go authentication/API:
keyboard claim, interrupted wizard, reviewed groups, freeze/restore feedback,
history, session revocation and no credentials in responses/localStorage.
Provider observations and runtime mutations in that browser fixture are synthetic
and labeled in the page. This does not prove real database onboarding or activation.

The preapproved local profile now has a real runtime backend for reads, freeze,
resume and restore, with control restart/session verification. See control-runtime.md.
Interactive connection/boundary/lock editing remains B14; G2 customer/remote release is not cleared.
The offline `pooltune-owner` CLI now provides password recovery and unclaimed
bootstrap-token replacement under exclusive control ownership, with atomic audit
and session revocation. See owner-recovery.md. Customer security review remains
open. Do not reset database state to recover access. There is no public reset endpoint and no hidden default password.
