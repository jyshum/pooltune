import { END, START, StateGraph, StateSchema } from "@langchain/langgraph";
import type { BaseCheckpointSaver } from "@langchain/langgraph";
import { z } from "zod";

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
});

export const proposalSchema = z.strictObject({
  outcome: z.enum(["propose", "no_change"]),
  run_id: z.string().min(1).max(64),
  table_oid: z.number().int().positive().max(4294967295),
  column_number: z.number().int().positive().max(32767),
  reason: z.string().min(1).max(1000),
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
