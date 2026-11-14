# Docker Deployment Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans inline.

**Goal:** Build and verify an initial Docker package without altering tuner behavior or the active native demo.
**Architecture:** Multi-stage build with non-root runtime and separate local PostgreSQL verification target. Docker CLI + Colima is installed. Application placement remains open.
**Tech Stack:** Go 1.27.3, Node 22, Debian Bookworm, PgBouncer 1.26.1, PostgreSQL 17, Docker/Compose.
**Spec:** ../specs/2026-10-25-docker-deployment-design.md

## Global constraints
Preserve native demo, original dirty checkout and website. No cloud benchmark. Keep secrets out of image layers. Do not change listener safety rules for packaging convenience.

## Review focus
Build-context secret leakage; missing runtime libraries; skipped rather than executed integration tests; container-local ownership locks; confusing package verification with a working cross-container deployment.

### Task 1: Package and verify the runtime
- [x] Add deny-by-default .dockerignore and explicit multi-stage Dockerfile.
- [x] Add verification Compose profile and fail-fast runner that requires PostgreSQL/PgBouncer and asserts the real integration test passes rather than skips.
- [x] Install chosen runtime after user response, build and run the verification target.
- [x] Check runtime user, payload, startup failure with no config and image layers for private files.

### Task 2: Application and dashboard networking
- [ ] Resolve application placement from user response.
- [ ] Write failing connection/authentication and shared-lock tests for chosen topology.
- [ ] Implement minimal explicit networking and mounts; preserve all provider checks.
- [ ] Verify SQL, shutdown/restart, dashboard login and persistent state in Compose.

### Task 3: Review and delivery
- [ ] Fresh whole-change review, fix important issues with focused regressions.
- [ ] Document exact supported topology and unverified provider/container gaps.
- [ ] Publish only after actual container verification.
