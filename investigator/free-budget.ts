import type pg from "pg";

const model = "claude-sonnet-5-5";
// Standard global pricing in millionths of a US dollar per token.
const inputRate = 2;
const outputRate = 10;

export class FreePlanLimit extends Error {
  constructor() { super("Free plan diagnosis budget reached"); }
}

// The provider SDK wraps transport errors in APIConnectionError.
export function isFreePlanLimit(error: unknown): boolean {
  while (error instanceof Error) {
    if (error instanceof FreePlanLimit) return true;
    error = error.cause;
  }
  return false;
}

export interface BudgetLedger {
  reserve(maximum: number): Promise<string | null>;
  settle(id: string, cost: number): Promise<void>;
  release(id: string): Promise<void>;
}

export function projectLedger(pool: pg.Pool, project: string, epoch: string): BudgetLedger {
  return {
    async reserve(maximum) {
      const result = await pool.query("SELECT pooltune_app.reserve_free_model($1,$2,$3) AS id", [project, epoch, maximum]);
      return result.rows[0].id;
    },
    async release(id) {
      await pool.query("DELETE FROM pooltune_app.free_model_usage WHERE id=$1 AND NOT settled", [id]);
    },
    async settle(id, cost) {
      await pool.query("UPDATE pooltune_app.free_model_usage SET micros=$2, settled=true WHERE id=$1 AND NOT settled", [id, cost]);
    },
  };
}

// Wrap the SDK transport so every actual request (including follow-ups) needs
// its own committed reservation. No model call occurs on a failed admission.
export function meteredFetch(ledger: BudgetLedger, upstream: typeof fetch = fetch): typeof fetch {
  return async (url, init) => {
    const request = new Request(url, init);
    const endpoint = new URL(request.url);
    if (endpoint.origin !== "https://api.anthropic.com" || endpoint.pathname !== "/v1/messages" || request.method !== "POST") {
      throw Error("Unpriced provider endpoint");
    }
    const payload = await request.text();
    const body = JSON.parse(payload);
    if (body.model !== model || !Number.isSafeInteger(body.max_tokens) || body.max_tokens < 1 || body.max_tokens > 1200 ||
        body.stream || body.cache_control || body.tools?.some((tool: {type?: string}) => tool.type && tool.type !== "custom") ||
        /"cache_control"\s*:|"type"\s*:\s*"(image|document|container_upload)"/.test(payload)) {
      throw Error("Unpriced provider request");
    }
    body.service_tier = "standard_only";
    body.inference_geo = "global";
    const wire = JSON.stringify(body);
    // Text-only messages: reserve one input token per UTF-8 byte of the whole
    // envelope, plus room for provider framing and tool instructions.
    const maximum = (Buffer.byteLength(wire) + 4096) * inputRate + body.max_tokens * outputRate;
    const receipt = await ledger.reserve(maximum);
    if (!receipt) throw new FreePlanLimit();

    // A timeout, interruption or missing receipt keeps the full reservation.
    // Settle before returning the response, even if schema parsing later fails.
    const headers = new Headers(request.headers);
    headers.delete("content-length");
    const response = await upstream(request.url, {
      method: "POST", headers, signal: request.signal, body: wire, redirect: "error",
    });
    if ([400, 401, 403, 404, 413, 429].includes(response.status)) {
      await ledger.release(receipt);
    } else if (response.ok) {
      const message = await response.clone().json();
      const usage = message.usage;
      if (usage && Number.isSafeInteger(usage.input_tokens) && usage.input_tokens >= 0 &&
          Number.isSafeInteger(usage.output_tokens) && usage.output_tokens >= 0 &&
          !usage.cache_creation_input_tokens && !usage.cache_read_input_tokens) {
        await ledger.settle(receipt, usage.input_tokens * inputRate + usage.output_tokens * outputRate);
      }
    }
    return response;
  };
}
