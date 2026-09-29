import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { SessionViewer } from '../src/components/SessionViewer';
import { I18nProvider } from '../src/i18n';
import { sessionService } from '../src/services/session';
let requests = 0;
sessionService.getToolCall = async (_root, _session, callId) => {
  requests++;
  return {callId, kind: 'read', title: callId, status: 'complete', content: [{type:'text',text:'REMOTE DETAIL '+callId+'\n'+('detail line\n'.repeat(80))}]};
};
const exchanges = Array.from({length: 30}, (_, i) => [
  {seq:i*2+1,role:'user',content:'User question '+i,timestamp:'2026-09-28T00:00:00Z'},
  {seq:i*2+2,role:'agent',content:'Agent answer '+i,timestamp:'2026-09-28T00:00:01Z'},
]).flat();
const exchange_aux = Object.fromEntries(Array.from({length:30},(_,i)=>[i*2+2,Array.from({length:100},(_,j)=>({seq:i*2+2,line:0,toolcall:{callId:`call-${i}-${j}`,kind:'read',status:'complete',title:`Tool ${i}-${j}`,meta:{}}}))]));
function Check() {
  const [session,setSession]=useState<any>({key:'virtual-test',exchanges,exchange_aux});
  const [target,setTarget]=useState(0);
  const [shown,setShown]=useState(true);
  Object.assign(window,{check:{requests:()=>requests,jump:(seq:number)=>setTarget(seq),toggle:()=>setShown(x=>!x),append:()=>setSession((s:any)=>({...s,exchanges:[...s.exchanges,{seq:s.exchanges.length+1,role:'agent',content:'APPENDED '+s.exchanges.length,timestamp:'2026-09-28T00:00:02Z'}]}))}});
  Object.assign((window as any).check, {
    replaceSession: (next: any) => { setTarget(0); setSession(next); },
    question: () => setSession((s: any) => ({...s, pending: true, exchanges: [...s.exchanges, {
      role: 'tool', content: '', toolCall: {
        callId: 'question', kind: 'ask_user', status: 'running',
        meta: {questions: [{question: 'Draft retention check'}]},
      },
    }]})),
    short: () => setSession({key:'short-test',exchanges:exchanges.slice(0,2),exchange_aux:{2:[
      {seq:2,line:0,toolcall:{callId:'edit',kind:'edit',status:'complete',title:'Large edit',content:[{type:'diff',path:'example.ts',oldText:'old line\n'.repeat(400),newText:'NEW DIFF LINE\n'.repeat(400)}]}},
      {seq:2,line:0,toolcall:{callId:'shell',kind:'execute',status:'complete',title:'Shell',meta:{source:'userShell',command:'echo ready'},content:[{type:'text',text:'SHELL READY'}]}},
    ]}}),
  });
  return <I18nProvider><div style={{height:'100vh',display:'flex',flexDirection:'column'}}>{shown && <SessionViewer session={session} rootId="test" targetSeq={target}/>}</div></I18nProvider>;
}
createRoot(document.getElementById('root')!).render(<Check/>);

Object.assign(window, { runStreamingTextChecks: async () => {
  const settle = () => new Promise(resolve => setTimeout(resolve, 80));
  for (const long of [false, true]) {
    const prefix = long ? exchanges : exchanges.slice(0, 1);
    let content = 'Stable paragraph\n\n```js\nconst answer = 42;\n```\n\nStreaming';
    const update = (step: number) => (window as any).check.replaceSession({
      key: `streaming-${long}`, exchange_aux: long ? exchange_aux : {},
      exchanges: [...prefix, {seq: 999, role: 'agent', content, timestamp: `2026-09-29T00:00:${String(step).padStart(2, '0')}Z`}],
    });
    update(0);
    await settle();
    const row = document.querySelector('[data-session-seq="999"]')!;
    const markdown = row?.querySelector('.markdown-viewer');
    const paragraph = markdown?.querySelector('p');
    const code = markdown?.querySelector('pre');
    if (!row || !markdown || !paragraph || !code) throw new Error('Missing streaming fixture nodes');
    for (let step = 1; step <= 16; step++) {
      content += ` chunk ${step}`;
      update(step);
      await settle();
      if (document.querySelector('[data-session-seq="999"]') !== row) throw new Error(`Streaming row remounted (virtual=${long})`);
      if (row.querySelector('.markdown-viewer') !== markdown || markdown.querySelector('p') !== paragraph || markdown.querySelector('pre') !== code) {
        throw new Error(`Unchanged Markdown nodes remounted (virtual=${long})`);
      }
      if (!markdown.textContent?.includes(`chunk ${step}`)) throw new Error('Streaming content did not update');
    }
  }
  return {passed: true, modes: ['short', 'virtual'], updatesPerMode: 16};
}});

Object.assign(window, { runScrollIntentChecks: async () => {
  const settle = (ms = 250) => new Promise(resolve => setTimeout(resolve, ms));
  const assert = (condition: boolean, message: string) => { if (!condition) throw new Error(message); };
  const el = [...document.querySelectorAll('div')].find(node => getComputedStyle(node).overflowY === 'auto')!;
  const gap = () => el.scrollHeight - el.clientHeight - el.scrollTop;
  await settle();
  for (const input of ['wheel', 'touch']) {
    el.scrollTop = el.scrollHeight;
    await settle();
    if (input === 'touch') {
      el.dispatchEvent(new TouchEvent('touchstart', {bubbles: true, touches: [new Touch({identifier: 1, target: el, clientY: 200})]}));
    }
    for (let step = 1; step <= 12; step++) {
      if (input === 'wheel') {
        el.dispatchEvent(new WheelEvent('wheel', {bubbles: true, deltaY: -5}));
      } else {
        el.dispatchEvent(new TouchEvent('touchmove', {bubbles: true, touches: [new Touch({identifier: 1, target: el, clientY: 200 + step * 5})]}));
      }
      el.scrollTop -= 5;
      await settle(40);
      assert(gap() >= 4, `${input}: small upward scroll snapped back to bottom`);
    }
    if (input === 'touch') el.dispatchEvent(new TouchEvent('touchend', {bubbles: true, touches: []}));
    assert(gap() >= 45, `${input}: slow upward scrolling made no progress`);
    const top = el.scrollTop;
    (window as any).check.append();
    window.dispatchEvent(new Event('mindfs:safe-area-updated'));
    await settle();
    assert(Math.abs(el.scrollTop - top) < 2, `${input}: append/viewport change stole reading position`);
    el.scrollTop = el.scrollHeight;
    await settle();
    (window as any).check.append();
    await settle();
    assert(gap() < 2, `${input}: returning to bottom did not resume following`);
  }
  return {passed: true, inputs: ['wheel', 'touch'], upwardSteps: 12};
}});

// Run via a browser console or agent-browser eval. No server/agent data is used.
Object.assign(window, { runVirtualizationChecks: async () => {
  const api = () => (window as any).check;
  const settle = () => new Promise(resolve => setTimeout(resolve, 400));
  const assert = (condition: boolean, message: string) => { if (!condition) throw new Error(message); };
  const scroller = () => [...document.querySelectorAll('div')].find(node => getComputedStyle(node).overflowY === 'auto')!;
  const rows = () => document.querySelectorAll('[data-index]').length;
  await settle();
  assert(rows() > 0 && rows() < 80, 'Only viewport rows should mount');
  const initialRows = rows();
  assert(scroller().scrollHeight - scroller().scrollTop - scroller().clientHeight < 2, 'Initial view should be at bottom');
  assert(requests === 0, 'Collapsed cards should not load remote details');
  api().jump(21);
  await settle();
  const target = document.querySelector('[data-session-seq="21"]')!;
  assert(!!target && target.getBoundingClientRect().top > 0 && target.getBoundingClientRect().bottom < innerHeight, 'Search target should be visible');
  const tool = [...document.querySelectorAll('button')].find(node => node.textContent?.trim() === 'Tool 10-0')!;
  tool.click();
  await settle();
  assert(requests === 1 && document.body.innerText.includes('REMOTE DETAIL call-10-0'), 'Expanding should load details');
  api().jump(41);
  await settle();
  assert(!document.body.innerText.includes('REMOTE DETAIL call-10-0'), 'Offscreen details should unmount');
  api().jump(21);
  await settle();
  assert(requests === 1 && document.body.innerText.includes('REMOTE DETAIL call-10-0'), 'Expansion and cached details should survive remount');
  const readingTop = scroller().scrollTop;
  api().append();
  await settle();
  assert(Math.abs(scroller().scrollTop - readingTop) < 2, 'Appending should preserve reading position');
  // Re-entering constructs a new viewer without mounting the full history.
  api().toggle();
  await settle();
  api().jump(0);
  api().toggle();
  await settle();
  assert(rows() > 0 && rows() < 80, 'Re-entry should remain virtualized');
  api().append();
  await settle();
  assert(scroller().scrollHeight - scroller().scrollTop - scroller().clientHeight < 2, 'Appending at bottom should follow the tail');
  const finalRows = rows();
  api().question();
  await settle();
  const draft = document.querySelector('textarea');
  assert(!!draft, 'Pending question should display its input');
  api().jump(21);
  await settle();
  assert(draft === document.querySelector('textarea') && draft!.isConnected, 'Pending question input should survive scrolling out of view');
  api().jump(0);
  api().short();
  await settle();
  assert(rows() === 0, 'Short sessions should use the regular layout');
  assert(!document.body.innerText.includes('NEW DIFF LINE'), 'Folded diff should not render');
  assert(document.body.innerText.includes('SHELL READY'), 'User shell should still expand by default');
  [...document.querySelectorAll('button')].find(node => node.textContent?.includes('Large edit'))!.click();
  await settle();
  assert(document.body.innerText.includes('NEW DIFF LINE'), 'Expanded diff should render');
  assert(requests === 1, 'Local diff and shell output should not request remote details');
  return {passed: true, toolCalls: 3000, initialRows, finalRows, detailRequests: requests};
}});
