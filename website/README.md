# Pooltune public site

Static site for the deterministic pool-tuning MVP. AI and business-priority scheduling are deferred and not shown.

Self-contained. It has its own `package.json` and lockfile and imports nothing from `ui/`, Go code or the root package.

## Build

```sh
npm ci
npm run build    # copy check, type check, Vite build, output check
npm run dev      # http://127.0.0.1:5173
```

Output goes to `dist/`. Asset paths are relative, so the folder works on any static host. No host is chosen and nothing has been deployed.

`npm run build` fails when:

- any file in `src/`, `index.html` or this README contains an em or en dash
- a run in `src/data/recorded-results.json` is missing provenance
- the built bundle loses the "Illustrative" label or uses root-absolute paths

## Page

1. Hero with the pixel wave (`src/components/PixelWave.tsx`, canvas, pauses offscreen, still frame on reduced motion).
2. How it works: a sticky scroll story (`src/components/ScrollStory.tsx`). GSAP ScrollTrigger maps scroll to five hand-written states. The panel is tagged Illustrative. It is not measured data.
3. Results: hidden until `recorded-results.json` has a valid run.
4. Works with: logo cards for each target over `src/components/PixelField.tsx`, a pixel swell seen from above that fades from grey troughs to cyan crests.
5. Get started: placeholder steps and terminal until the install path is ready.
6. Footer: one line of known limits.

## Where claims live

Every public claim is in `src/content.ts`:

- `targets`: the Works with cards, in display order. Per-target support status is not shown on this section; the footer's limits line covers it.
- `setup`: Get started copy. Replace the terminal placeholder in `App.tsx` with the real install command once it exists.
- `limits`: backup routing, hosted provider testing, no speed claims. Keep it until those gaps close.

## Recorded results (for Codex)

Only real runs from `src/data/recorded-results.json` are shown. It ships empty. Add runs like this:

```json
{
  "runs": [
    {
      "id": "fixed-vs-auto-v2",
      "title": "Short title",
      "kind": "measured",
      "recordedAt": "2026-10-25",
      "sourceCommit": "full or short git hash",
      "environment": "machine, PostgreSQL and PgBouncer versions",
      "workload": "what traffic was offered",
      "summary": "One or two plain sentences. Include unfavorable results.",
      "columns": ["Case", "Offered", "Success", "Timeout"],
      "rows": [["fixed_warm", 120, 120, 0]],
      "rawDataUrl": "link to raw data in the repo",
      "notes": ["Optional limits of the run"]
    }
  ],
  "video": { "url": "https://...", "title": "Pooltune demo", "recordedAt": "2026-10-25" }
}
```

- `kind` is `measured` for a direct run or `replay` for a replay of a recorded run. The page labels each one.
- Every row needs one cell per column. The first cell is the row label.
- When `video` is set, the hero's second button becomes "Watch the demo".
- Only add numbers that came from a real run with retained raw data.

## Dependencies

- React 19.3.1 and Vite 8.3.5, pinned to match `ui/`.
- GSAP 3.15.0 with ScrollTrigger: scroll story and reveals. Both respect reduced motion.
- thinking-orbs 0.3.2 (MIT, Jakub Antalik): the status orb on the Pooltune box in the scroll story. Mood to state mapping is in `src/components/TunerOrb.tsx`.
- @fontsource/nunito 5.3.0 (OFL-1.1): Nunito ExtraBold, latin subset only, for the lowercase `pooltune` wordmark. Bundled locally.
- Logos in `src/logos.ts`: four SVGs copied from the Iconify logos set (CC0). PgBouncer has no logo, so it uses the PostgreSQL elephant. Logos are trademarks of their owners.
- React Bits `SpotlightCard`, vendored in `src/reactbits/`. Unchanged except for removing the `use client` line. License: MIT plus Commons Clause, copyright David Haz. Commons Clause does not allow selling the components themselves.

No fonts, scripts or images load from other origins.
