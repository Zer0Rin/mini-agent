// Node 24+: node --test scripts/check-web.mjs (no test-only dependencies).
import test from 'node:test';
import assert from 'node:assert/strict';
import { parseSSE } from '../web/lib/sse.ts';
import { applyEvent, initialChatState } from '../web/lib/chat-state.ts';

test('UTF-8 split at every byte survives SSE and the reducer', async () => {
  const events = [
    {type:'agent_start'},
    {type:'message_start',message:{role:'assistant',content:''}},
    {type:'message_update',delta:{reasoning:'中文思考'}},
    {type:'message_update',delta:{content:'你好，世界🌏'}},
    {type:'message_end',message:{id:'saved',role:'assistant',content:'你好，世界🌏',reasoning:'中文思考'}},
    {type:'done'},
  ];
  const bytes = new TextEncoder().encode(events.map(e => `event: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`).join(''));
  const stream = new ReadableStream({start(controller) {for (const byte of bytes) controller.enqueue(Uint8Array.of(byte)); controller.close();}});
  let state = initialChatState();
  const actual = [];
  for await (const event of parseSSE(stream)) {actual.push(event); state = applyEvent(state,event);}
  assert.deepEqual(actual,events);
  assert.equal(state.messages[0].content,'你好，世界🌏');
  assert.equal(state.messages[0].reasoning,'中文思考');
  assert.equal(state.draft,null);
  assert.equal(state.running,false);
});

test('parallel tools retain independent results, including failure', () => {
  let state=initialChatState();
  for (const event of [
    {type:'agent_start'},
    {type:'tool_execution_start',tool_call_id:'a',tool_name:'datetime',args:'{}'},
    {type:'tool_execution_start',tool_call_id:'b',tool_name:'datetime',args:'{"tz":"invalid"}'},
    {type:'tool_execution_end',tool_call_id:'b',result:{content:'bad timezone',is_error:true}},
    {type:'tool_execution_end',tool_call_id:'a',result:{content:'2026',is_error:false}},
    {type:'error',error:'aborted'},
    {type:'done'},
  ]) state=applyEvent(state,event);
  assert.equal(state.tools.a.status,'ok');
  assert.equal(state.tools.b.status,'error');
  assert.equal(state.tools.b.result,'bad timezone');
  assert.equal(state.error,'aborted');
  assert.equal(state.running,false);
});

test('stream recovery retains only unsaved input', async () => {
  const { unsavedDraft } = await import('../web/lib/recovery.ts');
  const before = [{id:'old',role:'user',content:'same'}];
  assert.equal(unsavedDraft(before, 1, 'same'), 'same');
  assert.equal(unsavedDraft([...before,{id:'new',role:'user',content:'same'}], 1, 'same'), '');
  assert.equal(unsavedDraft([...before,{id:'other',role:'user',content:'another tab'}], 1, 'same'), 'same');
});
