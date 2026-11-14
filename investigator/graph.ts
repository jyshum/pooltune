import { END, START, StateGraph, StateSchema } from "@langchain/langgraph";
import type { BaseCheckpointSaver } from "@langchain/langgraph";
import { z } from "zod";
import {cronSchema,cronCause} from "./cron-schema.ts";

const planSchema = z.strictObject({
  node: z.string().max(60),
  examined_rows: z.number().int().nonnegative(),
  returned_rows: z.number().int().nonnegative(),
  shared_blocks: z.number().int().nonnegative(),
});

export const evidenceSchema = z.strictObject({
  run_id: z.string().min(1).max(64),
  observed_at: z.iso.datetime(),
  decision: z.enum(["investigate", "no_change", "insufficient_evidence"]),
  table_oid: z.number().int().positive().max(4294967295),
  column_number: z.number().int().positive().max(32767),
  lookup_column: z.literal("lookup"),
  covering_index: z.boolean(),
  plan: planSchema,
  cron: cronSchema.optional(),
  memory: z.strictObject({function_oid:z.number().int().positive(),temp_bytes:z.number().nonnegative(),internal_temp_bytes:z.number().nonnegative(),sort_nodes:z.number().int().positive(),max_concurrent_calls:z.number().int().positive(),candidate_bytes:z.number().int().positive(),aggregate_bytes:z.number().int().positive(),current_work_mem:z.string(),candidate_work_mem:z.string(),identity_verified:z.boolean()}).optional(),
  vacuum: z.strictObject({ heap_fetches: z.number().nonnegative(), scan_rows: z.number().int().positive(), conditions_clear: z.boolean() }).optional(),
  statistics: z.strictObject({
    correlated_columns: z.boolean(),
    cases: z.array(z.strictObject({ estimated_rows: z.number().int().positive(), actual_rows: z.number().int().positive() })).length(2),
  }).optional(),
  analyze: z.strictObject({ estimated_rows: z.number().int().positive(), actual_rows: z.number().int().positive(), sampled_lookup_fraction: z.number().min(0).max(1).optional() }).optional(),
}).refine(e => [e.analyze,e.statistics,e.vacuum,e.memory,e.cron].filter(Boolean).length <= 1, "Evidence must contain at most one repair cause.");

export const proposalSchema = z.strictObject({
  outcome: z.enum(["propose", "no_change"]),
  run_id: z.string().min(1).max(64),
  table_oid: z.number().int().positive().max(4294967295),
  column_number: z.number().int().positive().max(32767),
  repair: z.enum(["analyze", "statistics", "vacuum", "memory", "cron"]).optional(),
  reason: z.string().min(1).max(1000),
  model_call: z.strictObject({
    model: z.string().min(1), duration_ms: z.number().nonnegative(), requests: z.literal(1),
    input_tokens: z.number().int().nonnegative().optional(), output_tokens: z.number().int().nonnegative().optional(),
  }).optional(),
});

export type Evidence = z.infer<typeof evidenceSchema>;
export type Proposal = z.infer<typeof proposalSchema>;
export type EvidenceTool = "plan" | "indexes";
export type Investigator = (read: (tool: EvidenceTool) => unknown, enrollment: Omit<Evidence, "plan" | "covering_index">) => Promise<unknown>;

const state = new StateSchema({
  evidence: evidenceSchema,
  tools: z.array(z.enum(["plan", "indexes"])).default([]),
  proposal: proposalSchema.optional(),
});

function identity(evidence: Evidence) {
  return { run_id: evidence.run_id, table_oid: evidence.table_oid, column_number: evidence.column_number };
}

export function buildGraph(investigate: Investigator, checkpointer?: BaseCheckpointSaver) {
  return new StateGraph(state)
    .addNode("screen_evidence", ({ evidence }) => {
      if (evidence.cron) {
        if(evidence.decision === "investigate" && cronCause(evidence.cron)) return {};
        return {proposal:{...identity(evidence),outcome:"no_change" as const,reason:"Repeated attributed contention or enrolled schedule constraints are insufficient."}};
      }
      if (evidence.memory) {
        const m=evidence.memory;
        if (evidence.decision === "investigate" && evidence.plan.node === "Sort" && evidence.plan.examined_rows === 100000 && evidence.plan.returned_rows === 100000 && m.identity_verified && m.temp_bytes >= 8388608 && m.internal_temp_bytes >= 8388608 && m.sort_nodes === 1 && m.max_concurrent_calls === 3 && m.candidate_bytes === 33554432 && m.aggregate_bytes === 100663296 && m.current_work_mem === "4MB" && m.candidate_work_mem === "32MB") return {};
        return {proposal:{...identity(evidence),outcome:"no_change" as const,reason:"Attributed spill or bounded function-memory evidence is insufficient."}};
      }
      if (evidence.vacuum) {
        const v = evidence.vacuum;
        if (evidence.decision === "investigate" && evidence.covering_index && evidence.plan.node === "Index Only Scan" &&
            v.conditions_clear && v.heap_fetches >= 1000 && v.heap_fetches / v.scan_rows >= .5) return {};
        return { proposal: { ...identity(evidence), outcome: "no_change" as const, reason: "Measured visibility work does not justify VACUUM." } };
      }
      if (evidence.statistics) {
        const { correlated_columns, cases } = evidence.statistics;
        const strong = evidence.decision === "investigate" && correlated_columns &&
          cases.every(({ estimated_rows, actual_rows }) => Math.max(estimated_rows, actual_rows) >= 10 * Math.min(estimated_rows, actual_rows));
        if (strong) return {};
        return { proposal: { ...identity(evidence), outcome: "no_change" as const, reason: "Measured evidence does not support joint column statistics." } };
      }
      if (evidence.analyze) {
        const { estimated_rows, actual_rows } = evidence.analyze;
        const strong = evidence.decision === "investigate" && evidence.covering_index && Math.max(estimated_rows, actual_rows) >= 10 * Math.min(estimated_rows, actual_rows);
        if (strong) return {};
        return { proposal: { ...identity(evidence), outcome: "no_change" as const, reason: "The measured row estimate does not warrant a statistics refresh." } };
      }
      const { plan } = evidence;
      if (evidence.decision === "investigate" && !evidence.covering_index &&
          plan.node === "Seq Scan" && plan.examined_rows >= 10000 &&
          plan.returned_rows > 0 && plan.examined_rows / plan.returned_rows >= 10) {
        return {};
      }
      return { proposal: { ...identity(evidence), outcome: "no_change" as const, reason: "Evidence does not support a missing index." } };
    })
    .addNode("investigate", async ({ evidence }) => {
      const { plan, covering_index, ...enrollment } = evidence;
      const used = new Set<EvidenceTool>();
      const result = await investigate((tool) => {
        used.add(tool);
        return tool === "plan" ? plan : { covering_index };
      }, enrollment);
      const proposal = proposalSchema.parse(result);
      if (proposal.outcome === "propose" && (!used.has("plan") || !used.has("indexes"))) {
        throw new Error("A proposal needs both plan and catalog evidence.");
      }
      return { tools: [...used], proposal };
    })
    .addEdge(START, "screen_evidence")
    .addConditionalEdges("screen_evidence", ({ proposal }) => proposal ? END : "investigate", [END, "investigate"])
    .addEdge("investigate", END)
    .compile({ checkpointer });
}
