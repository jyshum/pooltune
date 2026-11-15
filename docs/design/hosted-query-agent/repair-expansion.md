# Compound indexes and the remaining investigator connections

Status: implementation and local qualification in progress. Hosted results will be recorded separately; this document does not claim production readiness.

## The problem this build solves

The first index repair handled one column. A query can need two columns together: each condition alone matches many records, but their combination identifies the record we want. Compound indexes were already part of V1. This build adds the first bounded two-column case.

The other five repair tools already worked in controlled staging tests. Their earlier model path received evidence for one known cause. This build connects those tools to the newer investigator, which compares causes and can ask Go for more evidence.

## How it works

Go collects evidence and identifies the enrolled target. Claude compares explanations and proposes a permitted repair, requests a named observation, or chooses no change. Go independently checks current conditions, applies the qualified change, measures its effect and saves the outcome. The model never supplies SQL or database credentials.

The compound case uses two ordered enrolled columns. The order travels with the evidence, model result and recovery record. Existing single-column jobs keep their old format. Supported compound construction is currently a nonunique B-tree on the fixed fixture's two equality columns; arbitrary customer query shapes remain unqualified.

Fresh evidence readers are deliberately smaller than repair experiments. They use read-only transactions and brief table locks that permit ordinary writes while preventing the table from being replaced during inspection. The statistics readers measure stale estimates or relationships between columns. Cleanup measures extra table checks. Function memory checks disk spill and the admitted memory allowance. Scheduling checks recorded complete periods, exact job identity and remaining deadlines.

## Trade-offs

One shared investigator keeps the tools, budgets and recovery rules consistent. Separate agents per repair would duplicate that coordination. Supplying enough evidence together still permits one Claude call; genuinely missing evidence can require another call within the existing budget.

The first compound case is intentionally bounded. General index search would support more queries but would require more candidate-selection, write-cost and recovery evidence. Passing this case does not complete every compound shape in V1.

Implementation and isolated local tests ran in parallel. Hosted repair trials stay sequential because the shared trial lock protects Rookery and overlapping experiments would distort the timings. These are engineering tests on owned synthetic data, not customer-population measurements.

## Evidence so far

- Local compound tests demonstrate roughly 25,000 matches for either individual key and one for their combination. Actual index execution reduces examined rows from 100,000 to one.
- A local mechanism run remained inconclusive on the unchanged 5 ms absolute benefit gate despite a large relative speedup. A valid index is not automatically a worthwhile repair.
- Real Sonnet passed 18/18 bounded diagnostic cases, including declining already-covered and unselective compound cases. These are retained/controlled evidence evaluations with no database mutation. [Cases](evidence/repair-expansion/model-cases.json) and [results](evidence/repair-expansion/model-results.jsonl).
- Independent reviews identified recovery and measurement defects before hosted execution. The progress log retains the findings and correction evidence.

## Remaining work in this batch

Finish recovery review, qualify each connected path with real Sonnet in Railway against Rookery, record read/write outcomes and timing breakdowns, verify cleanup, and update map colors only to match demonstrated scope. General customer onboarding, permissions, dashboard, sign-in and operating controls remain separate roadmap work.

## First hosted finding: the test disabled a useful PostgreSQL plan

The first compound run failed before repair. Its supposedly healthy query was still slow because an inherited test setting disabled bitmap index access. PostgreSQL can use an index to gather matching row locations, then fetch those rows together; that is a valid index plan, not a failed repair.

The detector correctly flagged the actual slowdown. Go recognized the existing index and made no change. A local reproduction changed only the disabled bitmap setting and reduced examined rows from 100,000 to one. The corrected driver preserves PostgreSQL's normal bitmap choice for the compound case. Thresholds and fixture data are unchanged. The failed run and cleanup evidence remain recorded; a new hosted run is required.
