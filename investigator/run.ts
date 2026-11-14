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
  if (!["postgres:", "postgresql:"].includes(url.protocol) || url.hostname !== "127.0.0.1" ||
      !url.port || !url.pathname.slice(1) || url.search !== "?sslmode=disable") {
    throw new Error("Expected Pooltune's local PostgreSQL state.");
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
