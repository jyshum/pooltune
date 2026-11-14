"""Render the shared design viewer with a separate, evidence-backed build-status layer."""
import argparse,json,re,subprocess,tempfile
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--skills-root',type=Path,default=Path.home()/'Desktop/code-folders/jared-skills');p.add_argument('--check',action='store_true');a=p.parse_args()
folder=Path(__file__).resolve().parent;base=folder.parent
status=json.loads((folder/'status.json').read_text());graph=json.loads((base/'graph.json').read_text())
assert set(status['nodes'])=={c['id'] for c in graph['components']},'Every card needs an explicit status'
assert set(status['next'])<=set(status['nodes'])
for n in status['nodes'].values():
 assert n['status'] in ['verified','partial','planned','external']
 assert n['scope'] and n['remaining'] and n['evidence']
 for ref in n['evidence']:assert (base.parents[2]/ref).is_file(),ref
with tempfile.TemporaryDirectory() as tmp:
 target=Path(tmp)/'design.html'
 subprocess.run(['python3',str(a.skills_root/'crocking/skills/design-map/scripts/render_floorplan.py'),str(base),'--output',str(target),'--repo-learning',str(a.skills_root/'repo-learning')],check=True,capture_output=True)
 html=target.read_text()
 # Upstream builds stamp temporary build directories and wall time. Use the
 # evidence commit's timestamp so identical canonical inputs reproduce exactly.
 match=re.search(r'<script>window.FLOORPLAN = (.*?);</script>',html)
 assert match,'Shared graph payload missing'
 projected=json.loads(match[1])
 stamp=subprocess.check_output(['git','show','-s','--format=%cI',status['sourceRevision']],cwd=base,text=True).strip()
 projected['meta'].update(generatedAt=stamp,scanAt=stamp,rootName=graph['id'])
 projected['meta']['exported']['at']=stamp
 payload=json.dumps(projected,ensure_ascii=False,separators=(',',':')).replace('<','\\u003c')
 html=html[:match.start(1)]+payload+html[match.end(1):]
 # The shared design exporter marks every card as unbuilt. This mixed-status
 # snapshot supplies its own explicit legend instead of that blanket annotation.
 html,count=re.subn(r'<script>/\* ui/06-blueprint\.js \*/[\s\S]*?</script>', '', html)
 assert count==1,'Shared blueprint annotation anchor changed'
 html=html.replace("PLAN ? 'Blueprint' : 'Codebase'", "'Build map'")

 def replace(old,new):
  global html
  assert html.count(old)==1,'Shared viewer anchor changed: '+old[:80]
  html=html.replace(old,new)
 data=json.dumps(status,ensure_ascii=False).replace('<','\\u003c').replace('\u2028','\\u2028').replace('\u2029','\\u2029')
 replace('</head>','<script>window.POOLTUNE_PROGRESS = '+data+';</script><style>'+(folder/'status.css').read_text()+'</style></head>')
 replace("return [meta?.stack, view?.changeStatus, view?.ownership].filter(Boolean).join(' · ');", "return [meta?.stack, window.POOLTUNE_PROGRESS.nodes[item.slice(2)] ? ({verified:'Staging verified',partial:'Partly built',planned:'Remaining',external:'External'})[window.POOLTUNE_PROGRESS.nodes[item.slice(2)].status] : '', view?.ownership].filter(Boolean).join(' · ');")
 replace('return `<div class="card ${cls}" style="--c:${colorOf(i)}">', 'return `<div class="card ${cls} pt-${window.POOLTUNE_PROGRESS.nodes[item.slice(2)]?.status || \'external\'}" data-progress-id="${h(item)}" style="--c:${colorOf(i)}">')
 # Extend the existing view through its public panel/header hooks; no upstream edits.
 js=(folder/'status.js').read_text();assert '</script' not in js.lower()
 replace('</body>','<script>'+js+'</script></body>')
 out=base/'design.html'
 if a.check:
  assert out.read_text()==html,'design.html is stale; run map/render.py'
  print('Map source, status coverage and generated output match')
 else:out.write_text(html);print('Updated '+str(out))
