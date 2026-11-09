'use strict';
let albumProposal=null,albumSelection=new Set(),albumStartPending=false,albumJobID='',albumJobRunning=false,albumTimer,albumRenderedResults=0;
function albumValue(value){return value===''?'(пусто)':value}
function albumChangeText(change){return change.key==='cover'?change.after:`${change.label}: ${albumValue(change.before)} → ${albumValue(change.after)}`}
function albumSelectedFields(){return [...$('albumChanges').querySelectorAll('input:checked')].map(input=>input.value)}
function albumTargetState(target,chosen){
  const changes=target.changes.filter(change=>chosen.has(change.key));
  if(!target.writable)return {eligible:false,text:'Формат доступен только для чтения'};
  if(changes.some(change=>change.key==='cover')&&!target.cover_writable)return {eligible:false,text:'Формат не поддерживает запись обложки — снимите её выбор'};
  if(!changes.length)return {eligible:false,text:'Выбранные значения уже совпадают'};
  return {eligible:true,text:changes.map(albumChangeText).join('\n')};
}
function offerAlbum(proposal){
  albumProposal=proposal;albumSelection=new Set(proposal.targets.map(t=>t.id));albumJobID='';albumJobRunning=false;albumRenderedResults=0;
  clearTimeout(albumTimer);
  $('albumHeading').textContent='Обновить другие треки альбома?';
  $('albumGroup').textContent=`${proposal.artist} · ${proposal.album}`;
  $('albumPreview').classList.remove('hidden');$('albumJobPanel').classList.add('hidden');
  $('closeAlbum').disabled=false;$('skipAlbum').disabled=false;
  $('albumToggleAll').disabled=false;
  $('albumJobResults').replaceChildren();$('albumChanges').replaceChildren();$('albumTargets').replaceChildren();
  for(const change of proposal.changes){
    const label=el('label','album-change'),input=el('input');input.type='checkbox';input.value=change.key;input.checked=true;
    const text=el('span');text.append(el('strong','',change.label),el('small','',albumChangeText(change)));
    label.append(input,text);
    if(change.key==='cover'&&change.after!=='Удалить обложку'){
      const img=el('img','album-cover');img.alt='Новая обложка';img.src=`/api/tracks/${proposal.source_id}/cover`;label.append(img);
    }
    input.onchange=refreshAlbumSelection;$('albumChanges').append(label);
  }
  for(const target of proposal.targets){
    const label=el('label','album-target');label.dataset.trackId=target.id;
    const input=el('input');input.type='checkbox';input.checked=true;input.setAttribute('aria-label',`Изменить: ${target.title}`);
    input.onchange=()=>{if(input.checked)albumSelection.add(target.id);else albumSelection.delete(target.id);refreshAlbumSelection()};
    const content=el('span','album-target-content');
    content.append(el('strong','',target.title),el('small','album-target-path',target.path),el('small','album-target-change'));
    label.append(input,content);$('albumTargets').append(label);
  }
  refreshAlbumSelection();$('albumDialog').showModal();
}
function refreshAlbumSelection(){
  if(!albumProposal)return;
  const chosen=new Set(albumSelectedFields());let count=0;
  for(let i=0;i<albumProposal.targets.length;i++){
    const target=albumProposal.targets[i],row=$('albumTargets').children[i],input=row.querySelector('input');
    const state=albumTargetState(target,chosen);
    input.disabled=!state.eligible||albumStartPending;
    input.checked=state.eligible&&albumSelection.has(target.id);
    row.classList.toggle('unavailable',!state.eligible);
    row.querySelector('.album-target-change').textContent=state.text;
    if(input.checked)count++;
  }
  $('albumSelectionCount').textContent=`Выбрано: ${count} из ${albumProposal.targets.length}`;
  $('applyAlbum').textContent=`Применить к ${countLabel(count,'треку','трекам','трекам')}`;
  $('applyAlbum').disabled=count===0||albumStartPending;
}
function closeAlbumDialog(){if(!albumStartPending&&!albumJobRunning){$('albumDialog').close();albumProposal=null;clearTimeout(albumTimer)}}
$('closeAlbum').onclick=closeAlbumDialog;$('skipAlbum').onclick=closeAlbumDialog;$('finishAlbum').onclick=closeAlbumDialog;
$('albumDialog').addEventListener('cancel',event=>{if(albumStartPending||albumJobRunning)event.preventDefault();else{albumProposal=null;clearTimeout(albumTimer)}});
$('albumToggleAll').onclick=()=>{
  const chosen=new Set(albumSelectedFields()),eligible=albumProposal.targets.filter(target=>albumTargetState(target,chosen).eligible);
  const all=eligible.every(target=>albumSelection.has(target.id));
  for(const target of eligible){if(all)albumSelection.delete(target.id);else albumSelection.add(target.id)}
  refreshAlbumSelection();
};
$('applyAlbum').onclick=safe(async()=>{
  if(!albumProposal||albumStartPending||albumJobRunning)return;
  const chosen=albumSelectedFields(),fieldSet=new Set(chosen);
  const ids=albumProposal.targets.filter(target=>albumSelection.has(target.id)&&albumTargetState(target,fieldSet).eligible).map(target=>target.id);
  if(!chosen.length||!ids.length)return;
  albumStartPending=true;setBusy(true);
  for(const input of $('albumChanges').querySelectorAll('input'))input.disabled=true;
  $('skipAlbum').disabled=true;$('closeAlbum').disabled=true;$('albumToggleAll').disabled=true;refreshAlbumSelection();
  try{
    const job=await api('/api/album-proposals/'+albumProposal.id+'/apply',{method:'POST',body:JSON.stringify({confirm:true,fields:chosen,track_ids:ids})});
    albumJobID=job.id;albumJobRunning=true;albumRenderedResults=0;
    $('albumPreview').classList.add('hidden');$('albumJobPanel').classList.remove('hidden');
    $('albumJobResults').replaceChildren();$('finishAlbum').disabled=true;$('cancelAlbumJob').disabled=false;
    renderAlbumJob(job);albumTimer=setTimeout(()=>pollAlbumJob(job.id),300);
  }catch(error){setBusy(false);throw error}
  finally{
    albumStartPending=false;
    if(!albumJobRunning){
      for(const input of $('albumChanges').querySelectorAll('input'))input.disabled=false;
      $('skipAlbum').disabled=false;$('closeAlbum').disabled=false;$('albumToggleAll').disabled=false;refreshAlbumSelection();
    }
  }
});
function renderAlbumJob(job){
  $('albumHeading').textContent=job.running?'Сохраняем изменения альбома':job.cancelled?'Обработка остановлена':'Обработка альбома завершена';
  $('albumJobSummary').textContent=`Обработано: ${job.completed} из ${job.total} · Сохранено: ${job.saved} · Без изменений: ${job.skipped} · Ошибок: ${job.failed} · Потоков: ${job.running?`${job.active} / ${job.workers}`:job.workers}`;
  $('albumJobCurrent').textContent=job.running?(job.active_files||[]).join('\n'):job.cancelled?`Не обработано: ${job.total-job.completed}. Уже сохранённые изменения остаются в файлах.`:'';
  for(const result of job.results.slice(albumRenderedResults)){
    const row=el('div','album-result '+result.state);
    row.append(el('strong','',result.title),el('small','',result.path));
    row.append(el('p','',result.state==='saved'?'Сохранено':result.state==='skipped'?'Без изменений':result.error));
    if(result.backup){const details=el('details');details.append(el('summary','','Резервная копия'),el('small','',result.backup));row.append(details)}
    $('albumJobResults').append(row);
  }
  albumRenderedResults=job.results.length;
  $('cancelAlbumJob').classList.toggle('hidden',!job.running);
  $('finishAlbum').disabled=job.running;$('closeAlbum').disabled=job.running;
}
async function pollAlbumJob(id){
  if(id!==albumJobID)return;
  try{
    const job=await api('/api/album-jobs/'+id);
    if(id!==albumJobID)return;
    albumJobRunning=job.running;renderAlbumJob(job);
    if(!job.running){setBusy(false);await Promise.all([loadTracks(),status()]);return}
  }catch(error){$('albumJobCurrent').textContent='Не удалось обновить состояние: '+error.message}
  if(albumJobRunning)albumTimer=setTimeout(()=>pollAlbumJob(id),800);
}
$('cancelAlbumJob').onclick=safe(async()=>{
  if(!albumJobID||!albumJobRunning)return;
  await api('/api/album-jobs/'+albumJobID+'/cancel',{method:'POST',body:'{}'});
  $('cancelAlbumJob').disabled=true;$('albumJobCurrent').textContent='Останавливаем обработку…';
});
