import { ChatAnthropic } from "@langchain/anthropic";
import { HumanMessage, SystemMessage } from "@langchain/core/messages";
import { buildGraph, proposalSchema, type Investigator } from "./graph.ts";

const investigationInstructions = "Investigate one enrolled synthetic PostgreSQL lookup. Propose a nonunique B-tree on the enrolled column only when a selective scan and absence of a covering index support it. Otherwise return no_change. Preserve the supplied identifiers and cite observed evidence. Do not supply SQL.";

function createAnthropicModel() {
  const modelName = process.env.POOLTUNE_ANTHROPIC_MODEL;
  if (!process.env.ANTHROPIC_API_KEY || !modelName) {
    throw new Error("Configure ANTHROPIC_API_KEY and POOLTUNE_ANTHROPIC_MODEL.");
  }
  return new ChatAnthropic({ model: modelName, maxTokens: 600, maxRetries: 0, clientOptions: { timeout: 8500 } });
}

// Both inputs are already in graph state. Send them together to save a model round trip.
export function buildSingleCallInvestigator(recommend: (messages: [SystemMessage, HumanMessage]) => Promise<unknown>): Investigator {
  return (read, enrollment) => recommend([
    new SystemMessage(investigationInstructions),
    new HumanMessage(JSON.stringify({ enrollment, plan: read("plan"), indexes: read("indexes") })),
  ]);
}

export const anthropicInvestigator = buildSingleCallInvestigator((messages) =>
  createAnthropicModel().withStructuredOutput(proposalSchema.omit({ repair: true }), { name: "index_recommendation" }).invoke(messages)
);

export const scriptedInvestigator: Investigator = async (read, evidence) => {
  read("plan");
  read("indexes");
  return { outcome: "propose", run_id: evidence.run_id, table_oid: evidence.table_oid,
    column_number: evidence.column_number, reason: "Scripted evaluation: selective scan on the enrolled lookup." };
};

export const graph = buildGraph(process.env.POOLTUNE_STUDIO_MODE === "scripted"
  ? scriptedInvestigator
  : anthropicInvestigator);
