import { readFileSync } from "node:fs";
import { PostgresSaver } from "@langchain/langgraph-checkpoint-postgres";
import pg from "pg";
import { buildGraph, evidenceSchema } from "./graph.ts";
import { anthropicInvestigator, scriptedInvestigator } from "./model.ts";

async function run() {
  const mode = process.argv[2];
  if (mode !== "anthropic" && mode !== "scripted") throw new Error("Expected an explicit investigator mode.");
  const evidence = evidenceSchema.parse(JSON.parse(readFileSync(0, "utf8")));
  const url = new URL(process.env.POOLTUNE_LAB_STATE_DSN ?? "");
  const staging = process.env.POOLTUNE_STAGING_PRIVATE_STATE === "1";
  const expectedRoute = staging
    ? url.hostname === "postgres.railway.internal" && url.port === "5432" && url.pathname === "/railway" && url.search === ""
    : url.hostname === "127.0.0.1" && !!url.port && /^\/[^/]+$/.test(url.pathname) && url.search === "?sslmode=disable";
  if (!["postgres:", "postgresql:"].includes(url.protocol) || !url.username || !url.password ||
      url.hash || !expectedRoute) {
    throw new Error("Expected Pooltune's configured PostgreSQL state.");
  }
  const pool = new pg.Pool({ connectionString: url.href, max: 1, connectionTimeoutMillis: 2500, statement_timeout: 3000 });
  try {
    const saver = new PostgresSaver(pool, undefined, { schema: "pooltune_graph" });
    await saver.setup();
    const graph = buildGraph(mode === "scripted" ? scriptedInvestigator : anthropicInvestigator, saver);
    const result = await graph.invoke({ evidence }, { configurable: { thread_id: evidence.run_id }, durability: "sync" });
    process.stdout.write(JSON.stringify(result.proposal));
  } finally {
    await pool.end();
  }
}

try {
  await run();
} catch {
  process.stderr.write("Pooltune investigation failed. Check state, model configuration and evidence.\n");
  process.exitCode = 1;
}
