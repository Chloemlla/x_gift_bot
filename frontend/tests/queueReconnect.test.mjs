import test from 'node:test';
import assert from 'node:assert/strict';
import { readQueueWithReconnect } from '../src/queueReconnect.ts';

test('reconnect keeps reading the original ticket across backend restart', async () => {
 const calls=[];
 const replies=[new Error('EOF'),{ok:false,status:503,data:{}},{ok:true,status:202,data:{ticket:'original',ahead:12}}];
 const result=await readQueueWithReconnect(async()=>{calls.push('original');const next=replies.shift();if(next instanceof Error)throw next;return next},new AbortController().signal,async()=>{});
 assert.equal(result.data.ticket,'original');assert.deepEqual(calls,['original','original','original']);
});
test('expired tickets and final errors are not endlessly retried',async()=>{
 let calls=0;
 const result=await readQueueWithReconnect(async()=>{calls++;return {ok:false,status:404,data:{}}},new AbortController().signal,async()=>{});
 assert.equal(result.status,404);assert.equal(calls,1);
});
test('retry timeout and cancellation stop reconnect',async()=>{
 let now=0;
 const result=await readQueueWithReconnect(async()=>({ok:false,status:503,data:{}}),new AbortController().signal,async()=>{now+=30000},()=>now,{maxWaitMs:60000});
 assert.equal(result.status,503);assert.equal(now,60000);
 const controller=new AbortController();controller.abort();
 await assert.rejects(readQueueWithReconnect(async()=>{throw Error('must not run')},controller.signal),{name:'AbortError'});
});

test('the normal queue keeps its ticket after more than a minute of network errors',async()=>{
 let elapsed=0,calls=0,retries=0;
 const result=await readQueueWithReconnect(async()=>{calls++;if(calls<5)throw new TypeError('Failed to fetch');return {ok:true,status:202,data:{ticket:'original'}}},new AbortController().signal,async()=>{elapsed+=30000},()=>elapsed,{onRetry:()=>{retries++}});
 assert.equal(result.data.ticket,'original');assert.equal(retries,4);assert.equal(elapsed,120000);
});

test('stored business failures are final, not reconnect loops',async()=>{
 for(const status of [409,422,500]){
  let calls=0;
  const result=await readQueueWithReconnect(async()=>{calls++;return {ok:false,status,data:{message:'final'}}},new AbortController().signal,async()=>{});
  assert.equal(result.status,status);assert.equal(calls,1);
 }
});
