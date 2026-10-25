# MVP source publication

This snapshot publishes the current managed gateway, local tuner, private dashboard and optional Tiger Data history. Claude's website is unchanged. Runtime source matches the working implementation used for the history preview; no new cloud run or performance claim accompanies publication.

The existing downloadable archive retains its original dirty-build metadata and checksum. It has not been rebuilt or retagged. Historical benchmark source hashes identify those historical snapshots, not this publication commit.

Publication-only changes: README build instructions, a current build-status heading, removal of an unavailable local report path, and replacement of live account identifiers in optional test harnesses and one historical Azure evidence file. Numerical outcomes and timing data are unchanged. The Azure subscription, resource group and server strings in quick-confirmation evidence use explicit example values.

## Optional live test targets

Cloud tests remain excluded by default behind the `managedlive` build tag and existing explicit authorization/window gates. Do not run them on a production database. They may change test pool settings and incur charges. Existing credentials stay in private files outside this repository.

- Azure background comparison: set `POOLTUNE_TEST_AZURE_SUBSCRIPTION`, `POOLTUNE_TEST_AZURE_RESOURCE_GROUP`, `POOLTUNE_TEST_AZURE_INSTANCE`, `POOLTUNE_TEST_AZURE_HOST` and `POOLTUNE_TEST_AZURE_USER`.
- Azure gateway comparison: set `POOLTUNE_TEST_AZURE_SUBSCRIPTION` and `POOLTUNE_TEST_AZURE_RESOURCE_GROUP`; other target fields come from its existing private profile.
- Supabase historical change-safety diagnostic and pressure diagnostic: set `POOLTUNE_TEST_SUPABASE_PROJECT`. The change-safety diagnostic deliberately tests live resizing and is not the supported burst strategy.

The deferred AI, priority and older runtime experiments are preserved as source history. Their presence does not enable or certify those features in the narrowed MVP. Current support and limits are in the root README.
