(() => {
 const F=window.Floorplan,P=window.POOLTUNE_PROGRESS;
 if(!F||!P) return;
 const h=F.h, labels={verified:'Verified scope',partial:'Partly built',planned:'Remaining',external:'External'};
 const badge=n=>`<span class="pt-status ${h(n.status)}">${h(n.statusLabel||labels[n.status])}</span>`;
 const link=id=>`<a class="pt-link" href="#s=${encodeURIComponent('n:'+id)}" data-go="n:${h(id)}">${h(F.info('n:'+id).name)}</a>`;
 const overview=()=>`<div class="sec"><h2>Build status</h2><p class="pt-copy">${h(P.summary)}</p><div class="pt-legend">${['verified','partial','planned'].map(status=>badge({status})).join('')}</div><p class="pt-copy">${h(P.colorMeaning)}</p><h3>What changed from the old map?</h3><p class="pt-copy">The detector, saved queue, consumer, action journal and six repair cases now run in staging. The approved Go + LangGraph architecture remains. All six bounded diagnostic repair paths passed hosted Sonnet. A separate hosted run requested fresh Go evidence. Connecting ordinary project queries and building the customer product remain.</p><h3>Next work</h3><p class="pt-copy">Proposed sequence within the existing roadmap. Each item still needs its detailed implementation plan.</p>${P.next.map(link).join('')}<h3>What does one known cause mean?</h3><p class="pt-copy">The earlier repair tests selected one problem category. The new diagnostic graph can compare multiple observations and request Go evidence. It passed 18 bounded replay cases and all six owned hosted repair paths. Ordinary customer evidence collection and representative mixed-cause workloads remain unfinished.</p><p class="pt-copy">Source evidence through ${h(P.sourceRevision.slice(0,7))}. This map is a snapshot, not live agent execution.</p></div>`;
 F.addPanelSection('overview',()=>({top:true,html:overview()}));
 F.addPanelSection('item',ref=>{
  const n=typeof ref==='string'&&ref.startsWith('n:')?P.nodes[ref.slice(2)]:null;
  if(!n)return '';
  return {top:true,html:`<div class="sec" data-pooltune-status>${badge(n)}<h3>Current scope</h3><p class="pt-copy">${h(n.scope)}</p>${n.remainingBuild?`<h3>Build next</h3><p class="pt-copy">${h(n.remainingBuild)}</p><h3>Verify next</h3><p class="pt-copy">${h(n.remainingVerification)}</p>`:`<h3>Still to do / limits</h3><p class="pt-copy">${h(n.remaining)}</p>`}<details><summary>Source references</summary>${n.evidence.map(path=>`<p class="pt-copy">${h(path)}</p>`).join('')}</details></div>`};
 });
 F.addHeaderButton({id:'pooltune-progress-button',label:'Build status',icon:'feature',title:'See completed work, gaps and next steps',onClick:()=>F.showPanel(overview())});
 F.refreshPanel();
})();
