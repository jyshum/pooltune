import { ChatAnthropic } from "@langchain/anthropic";
import { tool } from "@langchain/core/tools";
import { z } from "zod";
import { buildGraph, proposalSchema, type Investigator } from "./graph.ts";

const selectEvidence = tool(() => "", {
  name: "inspect_index_evidence",
  description: "Read the measured query plan and enrolled table's covering index check prepared by Go.",
  schema: z.object({}),
});

export const anthropicInvestigator: Investigator = async (read, enrollment) => {
  const modelName = process.env.POOLTUNE_ANTHROPIC_MODEL;
  if (!process.env.ANTHROPIC_API_KEY || !modelName) {
    throw new Error("Configure ANTHROPIC_API_KEY and POOLTUNE_ANTHROPIC_MODEL.");
  }
  const model = new ChatAnthropic({ model: modelName, maxTokens: 600, maxRetries: 0, clientOptions: { timeout: 8500 } });
  const request = "You investigate a single enrolled synthetic PostgreSQL lookup. Inspect the measured plan and catalog before recommending any index. A sequential scan can be appropriate. Propose a nonunique B-tree on the single enrolled column only if evidence shows avoidable work and no covering index. Otherwise return no_change. Return only the required structured recommendation with the original identifiers. Never supply SQL.";
  const call = await model.bindTools([selectEvidence], { tool_choice: selectEvidence.name }).invoke(request);
  if (call.tool_calls?.length !== 1 || call.tool_calls[0]?.name !== selectEvidence.name) {
    throw new Error("Expected one evidence inspection call.");
  }
  const observed = { enrollment, plan: read("plan"), indexes: read("indexes") };
  return model.withStructuredOutput(proposalSchema, { name: "index_recommendation" })
    .invoke([["system", request], ["human", JSON.stringify(observed)]]);
};

export const graph = buildGraph(anthropicInvestigator);
