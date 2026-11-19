import assert from "node:assert/strict";
import test from "node:test";
import { ChatAnthropic } from "@langchain/anthropic";
import { isFreePlanLimit, meteredFetch, type BudgetLedger } from "./free-budget.ts";

const url = "https://api.anthropic.com/v1/messages";
const model = "claude-sonnet-5-5";
const init = {method:"POST",body:JSON.stringify({model,max_tokens:600,messages:[{role:"user",content:"lookup"}]})};

test("actual SDK request reserves before sending and settles provider tokens", async () => {
  const events: string[] = [];
  const ledger: BudgetLedger = {
    async release(){assert.fail();},
    async reserve(maximum) { assert.ok(maximum > 6000); events.push("reserve"); return "17"; },
    async settle(id, cost) {assert.equal(id,"17"); assert.equal(cost,5000);events.push("settle");},
  };
  const transport = meteredFetch(ledger, async (_, options) => {
    events.push("provider");
    assert.equal(JSON.parse(String(options?.body)).service_tier,"standard_only");
    return Response.json({id:"msg_test",type:"message",role:"assistant",model,content:[{type:"text",text:"ok"}],
      stop_reason:"end_turn",usage:{input_tokens:1500,output_tokens:200}});
  });
  const client = new ChatAnthropic({model,apiKey:"local-test",maxTokens:600,maxRetries:0,clientOptions:{fetch:transport}});
  await client.invoke("lookup");
  assert.deepEqual(events,["reserve","provider","settle"]);
});

test("an exhausted budget and an unknown model cannot send a request", async () => {
  let calls=0;
  const transport=meteredFetch({async release(){assert.fail();},async reserve(){calls++;return null;},async settle(){assert.fail();}},async()=>assert.fail());
  await assert.rejects(transport(url,init),/budget reached/);
  await assert.rejects(transport(url,{...init,body:init.body.replace(model,"unpriced-model")}),/Unpriced/);
  assert.equal(calls,1);
});

test("unknown outcome keeps the committed reservation",async()=>{
  const transport=meteredFetch({async release(){assert.fail();},async reserve(){return "2";},async settle(){assert.fail();}},async()=>{throw Error("timeout");});
  await assert.rejects(transport(url,init),/timeout/);
});


test("provider rejection restores the credit and reserved money", async () => {
  let released = "";
  const transport = meteredFetch({
    async reserve() { return "8"; },
    async settle() { assert.fail(); },
    async release(id) { released = id; },
  }, async () => new Response(null, {status: 429}));
  assert.equal((await transport(url, init)).status, 429);
  assert.equal(released, "8");
});

test("budget denial stays recognizable through the provider SDK", async () => {
  const transport = meteredFetch({
    async reserve() { return null; }, async release() { assert.fail(); }, async settle() { assert.fail(); },
  }, async () => assert.fail());
  const client = new ChatAnthropic({model,apiKey:"local-test",maxTokens:600,maxRetries:0,clientOptions:{fetch:transport}});
  await assert.rejects(client.invoke("lookup"), isFreePlanLimit);
});
