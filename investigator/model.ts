import { ChatAnthropic } from "@langchain/anthropic";
import { HumanMessage, SystemMessage, ToolMessage } from "@langchain/core/messages";
import { tool } from "@langchain/core/tools";
import { z } from "zod";
import { buildGraph, proposalSchema, type Investigator } from "./graph.ts";

const evidenceRequest = z.strictObject({
  evidence: z.enum(["plan", "indexes", "both"]),
});

const selectEvidence = tool(() => "", {
  name: "inspect_index_evidence",
  description: "Choose which measured evidence to inspect. Request both plan and indexes if considering an index.",
  schema: evidenceRequest,
});

export const anthropicInvestigator: Investigator = async (read, enrollment) => {
  const modelName = process.env.POOLTUNE_ANTHROPIC_MODEL;
  if (!process.env.ANTHROPIC_API_KEY || !modelName) {
    throw new Error("Configure ANTHROPIC_API_KEY and POOLTUNE_ANTHROPIC_MODEL.");
  }
  const model = new ChatAnthropic({ model: modelName, maxTokens: 600, maxRetries: 0, clientOptions: { timeout: 8500 } });
  const messages = [
    new SystemMessage("Investigate one enrolled synthetic PostgreSQL lookup. Select the evidence you need. Propose a nonunique B-tree on the enrolled column only when a selective scan and absence of a covering index support it. Otherwise return no_change. Preserve the supplied identifiers and cite observed evidence. Do not supply SQL."),
    new HumanMessage(JSON.stringify(enrollment)),
  ];
  const response = await model.bindTools([selectEvidence], { tool_choice: selectEvidence.name }).invoke(messages);
  const [request] = response.tool_calls ?? [];
  if (response.tool_calls?.length !== 1 || request?.name !== selectEvidence.name || !request.id) {
    throw new Error("Expected one evidence inspection call.");
  }
  const { evidence } = evidenceRequest.parse(request.args);
  const observed: Record<string, unknown> = {};
  if (evidence !== "indexes") observed.plan = read("plan");
  if (evidence !== "plan") observed.indexes = read("indexes");
  return model.withStructuredOutput(proposalSchema, { name: "index_recommendation" }).invoke([
    ...messages, response, new ToolMessage({ content: JSON.stringify(observed), tool_call_id: request.id }),
  ]);
};

export const scriptedInvestigator: Investigator = async (read, evidence) => {
  read("plan");
  read("indexes");
  return { outcome: "propose", run_id: evidence.run_id, table_oid: evidence.table_oid,
    column_number: evidence.column_number, reason: "Scripted evaluation: selective scan on the enrolled lookup." };
};

export const graph = buildGraph(process.env.POOLTUNE_STUDIO_MODE === "scripted"
  ? scriptedInvestigator
  : anthropicInvestigator);
