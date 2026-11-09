'use strict';
const $=id=>document.getElementById(id);
const fields=['title','artist','album','album_artist','date','genre','track','disc','composer','comment'];
let selected=null,dirty=false,coverAction='keep',coverData=null,offset=0,total=0,order='asc',folder='',wasScanning=false,listRevision=0,selectionRevision=0,busy=false,toastTimer,pendingSelection=null;
const form=$('editForm');
let originalFieldValues={};
let view='tracks', skippedOffset=0, skippedTotal=0, skippedRevision=0, themePreference='system';
let clearing=false,statusRevision=0,deleting=false,deleteTrackTarget=null;
let sources=[],sourcesRevision=0,scanRunning=false,activeSource='',scanStarting=false;
const systemTheme=window.matchMedia('(prefers-color-scheme: dark)');
async function api(path,options={}){const res=await fetch(path,{...options,headers:{'Content-Type':'application/json',...options.headers}});if(!res.ok){let message;try{message=(await res.json()).error}catch{message=`Ошибка HTTP ${res.status}`}throw Error(message)}return res.json()}
function toast(message,error=false){clearTimeout(toastTimer);$('toast').textContent=message;$('toast').className='toast'+(error?' error':'');toastTimer=setTimeout(()=>$('toast').classList.add('hidden'),error?12000:6500)}
const safe=fn=>(...args)=>Promise.resolve().then(()=>fn(...args)).catch(e=>toast(e.message,true));
function el(tag,className,text){const node=document.createElement(tag);if(className)node.className=className;if(text!==undefined)node.textContent=text;return node}
function duration(sec){return `${Math.floor(sec/60)}:${String(Math.floor(sec%60)).padStart(2,'0')}`}
function coverURL(t){return `/api/tracks/${t.id}/cover?v=${t.mod_time}`}
function setDirty(value){dirty=value;$('dirtyIndicator').classList.toggle('hidden',!dirty);$('save').disabled=!dirty||busy||!selected?.writable}
function refreshEditorChanges(){
  let tagsChanged=false;
  for(const key of fields){
    const input=form.elements[key];
    const changed=!!selected&&input.value!==originalFieldValues[key];
    input.classList.toggle('field-changed',changed);
    if(changed){
      input.title=`Исходное значение: ${originalFieldValues[key]||'(пусто)'}`;
      input.setAttribute('aria-describedby','changedFieldHint');
    }else{
      input.removeAttribute('title');input.removeAttribute('aria-describedby');
    }
    tagsChanged||=changed;
  }
  const coverChanged=!!selected&&(coverAction==='replace'||(coverAction==='remove'&&selected.has_cover));
  setDirty(tagsChanged||coverChanged);
}
function setBusy(value){busy=value;form.querySelectorAll('input,textarea,button').forEach(n=>n.disabled=value);fields.forEach(key=>form.elements[key].disabled=value||!selected?.writable);$('deleteTrack').disabled=value||!selected;$('coverFile').disabled=value||!selected?.cover_writable;$('removeCover').disabled=value||!selected?.cover_writable;$('lookup').disabled=value||!selected?.writable;setDirty(dirty);$('save').textContent=value?(deleting?'Удаляем…':'Сохраняем…'):'Сохранить в файл'}
function showCover(src){const p=$('coverPreview');p.replaceChildren();if(src){const img=el('img');img.src=src;img.alt='Обложка альбома';p.append(img)}else p.textContent='♪'}
async function loadTracks(){
  const revision=++listRevision;
  const q=new URLSearchParams({q:$('search').value,artist:$('artistFilter').value,album:$('albumFilter').value,cover:$('coverFilter').value,sort:$('sort').value,order,offset});
  const page=await api('/api/tracks?'+q);
  if(revision!==listRevision)return;
  total=page.total;
  if(total>0&&offset>=total){offset=Math.floor((total-1)/100)*100;return loadTracks()}
  const container=$('tracks');container.replaceChildren();
  for(const track of page.tracks){
    const button=el('button','track'+(track.id===selected?.id?' selected':''));
    button.dataset.id=track.id;
    button.setAttribute('aria-label',`${track.tags.title||track.name} — ${track.tags.artist||'Без исполнителя'}`);
    const main=el('div','track-main'),thumb=el('span','thumb');
    if(track.has_cover){const img=el('img');img.src=coverURL(track);img.alt='';img.loading='lazy';thumb.append(img)}else thumb.textContent='♪';
    const text=el('div','track-text');
    text.append(el('div','track-title',track.tags.title||track.name),el('div','track-artist',track.tags.artist||'Без исполнителя'));
    main.append(thumb,text);
    button.append(main,el('div','track-album',track.tags.album||'Без альбома'),el('div','track-time',duration(track.duration)));
    button.onclick=safe(()=>requestSelection(track.id));container.append(button);
  }
  $('empty').classList.toggle('hidden',total>0);$('tracks').classList.toggle('hidden',total===0);
  const filtered=$('search').value||$('artistFilter').value||$('albumFilter').value||$('coverFilter').value;
  $('empty').querySelector('h2').textContent=filtered?'Ничего не найдено':'Музыка начинается с папки';
  $('empty').querySelector('p').textContent=filtered?'Измените поисковый запрос или сбросьте фильтры.':'Выберите папку. FFTagger найдёт аудиофайлы, прочитает теги и соберёт вашу коллекцию.';
  $('emptyChoose').classList.toggle('hidden',!!filtered);
  $('resultCount').textContent=countLabel(total,'трек','трека','треков');
  $('pageInfo').textContent=total?`${offset+1}–${Math.min(offset+100,total)} из ${total}`:'';
  $('prev').disabled=offset===0;$('next').disabled=offset+100>=total;
  updateNavigation();
}
async function requestSelection(id){if(busy||$('deleteTrackDialog').open)return;if(dirty){pendingSelection=id;$('discardDialog').showModal();return}await selectTrack(id)}
async function selectTrack(id){const revision=++selectionRevision;const track=await api('/api/tracks/'+id);if(revision!==selectionRevision)return;selected=track;renderEditor();document.querySelectorAll('.track').forEach(n=>n.classList.toggle('selected',Number(n.dataset.id)===id))}
function renderEditor(){
  $('editorEmpty').classList.add('hidden');form.classList.remove('hidden');
  fields.forEach(key=>form.elements[key].value=selected.tags[key]||'');
  // Compare displayed values: the browser normalizes textarea line endings
  // and removes line breaks from single-line inputs when assigning their value.
  originalFieldValues=Object.fromEntries(fields.map(key=>[key,form.elements[key].value]));
  $('editTitle').textContent=selected.tags.title||selected.name;
  $('editArtist').textContent=selected.tags.artist||'Без исполнителя';
  $('formatBadge').textContent=selected.codec.toUpperCase();$('filePath').textContent=selected.path;
  $('audioDetails').textContent=`${duration(selected.duration)} · ${selected.sample_rate} Гц · ${selected.channels} кан. · ${(selected.size/1048576).toFixed(1)} МБ`;
  $('rawTags').textContent=JSON.stringify(selected.raw_tags,null,2);
  $('readOnly').classList.toggle('hidden',selected.writable);
  $('readOnly').textContent='Этот контейнер доступен для чтения. Безопасная запись тегов через FFmpeg не поддерживается.';
  coverAction='keep';coverData=null;$('coverFile').value='';
  showCover(selected.has_cover?coverURL(selected):null);
  setBusy(false);fields.forEach(key=>form.elements[key].disabled=!selected.writable);
  refreshEditorChanges();
}
form.addEventListener('input',event=>{if(fields.includes(event.target.name))refreshEditorChanges()});
form.addEventListener('submit',e=>{e.preventDefault();if(view==='tracks')safe(save)()});
async function save(){
  if(!selected||!dirty||busy||$('deleteTrackDialog').open)return;
  const tags=Object.fromEntries(fields.map(k=>[k,form.elements[k].value]));
  let result;
  setBusy(true);
  try{
    result=await api('/api/tracks/'+selected.id,{method:'PUT',body:JSON.stringify({tags,expected_mod_time:selected.mod_time,cover_action:coverAction,cover:coverData})});
    selected=result.track;renderEditor();setBusy(true);
    try{await loadTracks()}catch(error){toast(error.message,true)}
  }finally{setBusy(false)}
  toast(result.suggestion_warning||'Теги сохранены. Резервная копия: '+result.backup,!!result.suggestion_warning);
  if(result.album_suggestion)offerAlbum(result.album_suggestion);
}
$('discard').onclick=()=>{if(selected&&!busy)renderEditor()};
function closeDeleteTrack(){if(!deleting){$('deleteTrackDialog').close();deleteTrackTarget=null}}
$('deleteTrack').onclick=()=>{
  if(!selected||busy||clearing)return;
  selectionRevision++;
  deleteTrackTarget={id:selected.id,path:selected.path,mod_time:selected.mod_time};
  $('deleteTrackTitle').textContent=selected.tags.title||selected.name;
  $('deleteTrackPath').textContent=selected.path;
  $('deleteTrackUnsaved').classList.toggle('hidden',!dirty);
  $('deleteTrackError').classList.add('hidden');
  $('deleteTrackDialog').showModal();
};
$('cancelDeleteTrack').onclick=closeDeleteTrack;
$('deleteTrackDialog').addEventListener('cancel',event=>{if(deleting)event.preventDefault();else deleteTrackTarget=null});
$('confirmDeleteTrack').onclick=async()=>{
  if(!deleteTrackTarget||deleting)return;
  const target=deleteTrackTarget;
  deleting=true;setBusy(true);
  $('confirmDeleteTrack').disabled=true;$('cancelDeleteTrack').disabled=true;
  $('deleteTrackError').classList.add('hidden');$('deleteTrackProgress').classList.remove('hidden');
  try{
    const result=await api('/api/tracks/'+target.id,{method:'DELETE',body:JSON.stringify({confirm:true,expected_path:target.path,expected_mod_time:target.mod_time})});
    listRevision++;selectionRevision++;statusRevision++;skippedRevision++;
    selected=null;pendingSelection=null;coverData=null;coverAction='keep';originalFieldValues={};
    form.reset();refreshEditorChanges();showCover(null);form.classList.add('hidden');$('editorEmpty').classList.remove('hidden');
    // Remove the row immediately, even if refreshing the catalog later fails.
    document.querySelectorAll('.track').forEach(row=>{if(Number(row.dataset.id)===target.id)row.remove()});
    total=Math.max(0,total-1);if(total===0)offset=0;
    $('deleteTrackDialog').close();deleteTrackTarget=null;
    const message=result.warning||(result.already_missing?'Файл уже отсутствовал. Запись удалена из библиотеки.':'Трек удалён из папки и библиотеки.');
    try{await loadTracks();await status();toast(message,!!result.warning)}
    catch(error){toast(message+' Не удалось обновить список: '+error.message,true)}
  }catch(error){
    $('deleteTrackError').textContent=error.message;$('deleteTrackError').classList.remove('hidden');
  }finally{
    deleting=false;setBusy(false);
    $('confirmDeleteTrack').disabled=false;$('cancelDeleteTrack').disabled=false;
    $('deleteTrackProgress').classList.add('hidden');
  }
};

$('coverFile').onchange=safe(async()=>{const file=$('coverFile').files[0];if(!file)return;if(file.size>16*1048576)throw Error('Обложка должна быть не больше 16 МБ');if(!['image/jpeg','image/png'].includes(file.type))throw Error('Выберите JPEG или PNG');const url=await new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(reader.result);reader.onerror=()=>reject(Error('Не удалось прочитать обложку'));reader.readAsDataURL(file)});coverData=url.split(',')[1];coverAction='replace';showCover(url);refreshEditorChanges()});
$('removeCover').onclick=()=>{coverAction='remove';coverData=null;showCover(null);refreshEditorChanges()};
async function scan(path){
  if(!path||scanStarting||clearing)return;
  scanStarting=true;updateSourceState();
  try{
    const source=await api('/api/sources',{method:'POST',body:JSON.stringify({path})});
    await loadSources();
    await api('/api/scan',{method:'POST',body:JSON.stringify({path:source.path})});
    folder=source.path;$('folderName').textContent=folder;$('folderPath').value=folder;$('rescan').classList.remove('hidden');
    await status();await loadSources();await loadCurrentView();
  }finally{scanStarting=false;updateSourceState()}
}
async function chooseFolder(){$('chooseFolder').disabled=true;$('emptyChoose').disabled=true;try{const result=await api('/api/pick',{method:'POST',body:'{}'});await scan(result.path)}finally{updateSourceState()}}
$('chooseFolder').onclick=safe(chooseFolder);$('emptyChoose').onclick=safe(chooseFolder);$('scanPath').onclick=safe(()=>scan($('folderPath').value));$('rescan').onclick=safe(()=>scan(folder));$('cancelScan').onclick=safe(async()=>{await api('/api/scan/cancel',{method:'POST',body:'{}'});$('cancelScan').disabled=true});
async function status(){
  if(clearing)return;
  const revision=++statusRevision;
  const data=await api('/api/status');
  if(revision!==statusRevision||clearing)return;
  $('dependency').classList.toggle('hidden',data.ready);
  $('dbLocation').textContent=data.database;$('toolLocation').textContent=data.ffmpeg||'Не найден';
  $('databaseWarning').textContent=data.database_warning||'';
  $('databaseWarning').classList.toggle('hidden',!data.database_warning);
  $('navCount').textContent=data.counts.tracks;
  $('noCoverCount').textContent=data.counts.without_cover;
  $('skippedCount').textContent=data.counts.skipped;
  const p=data.scan;
  scanRunning=p.running;activeSource=p.root||'';updateSourceState();
  if(p.root){
    folder=p.root;$('folderName').textContent=folder;$('rescan').classList.remove('hidden');
    $('scanPanel').classList.remove('hidden');$('scanPanel').classList.toggle('done',!p.running);
    $('scanTitle').textContent=p.running?'Сканирование коллекции':p.cancelled?'Сканирование остановлено':'Сканирование завершено';
    $('scanCurrent').textContent=p.current||p.root;
    $('scanCurrent').title=(p.active_files||[]).join('\n');
    $('scanStats').textContent=`Проверено: ${p.visited} · Добавлено / обновлено: ${p.imported} · Без изменений: ${p.unchanged} · Пропущено: ${p.skipped} · Потоков: ${p.running?`${p.active} / ${p.workers}`:p.workers}`;
    $('cancelScan').classList.toggle('hidden',!p.running);$('cancelScan').disabled=!p.running;
    $('scanErrors').classList.toggle('hidden',!p.errors?.length);$('scanErrors').querySelector('pre').textContent=(p.errors||[]).join('\n');
  }
  if(p.running||wasScanning)await loadCurrentView();
  wasScanning=p.running;
}
$('clearLibrary').onclick=()=>{
  if(busy||clearing){toast('Дождитесь завершения текущей операции.',true);return}
  $('clearUnsaved').classList.toggle('hidden',!dirty);
  $('clearDialog').showModal();
};
$('cancelClear').onclick=()=>{if(!clearing)$('clearDialog').close()};
$('clearDialog').addEventListener('cancel',event=>{if(clearing)event.preventDefault()});
$('confirmClear').onclick=safe(async()=>{
  if(clearing)return;
  clearing=true;statusRevision++;listRevision++;selectionRevision++;skippedRevision++;
  for(const id of ['confirmClear','cancelClear','clearLibrary'])$(id).disabled=true;
  $('clearProgress').classList.remove('hidden');
  try{
    const result=await api('/api/library/clear',{method:'POST',body:JSON.stringify({confirm:true})});
    selected=null;pendingSelection=null;coverData=null;coverAction='keep';setDirty(false);
    offset=0;total=0;skippedOffset=0;skippedTotal=0;folder='';wasScanning=false;scanRunning=false;activeSource='';
    listRevision++;selectionRevision++;skippedRevision++;statusRevision++;
    form.reset();originalFieldValues={};refreshEditorChanges();form.classList.add('hidden');$('editorEmpty').classList.remove('hidden');
    $('tracks').replaceChildren();$('skippedFiles').replaceChildren();
    $('scanPanel').classList.add('hidden');$('rescan').classList.add('hidden');
    $('folderName').textContent='Добавьте папку с вашей музыкой';$('folderPath').value='';
    for(const id of ['search','artistFilter','albumFilter','coverFilter','skippedSearch','skippedKind'])$(id).value='';
    for(const id of ['chooseFolder','scanPath','emptyChoose'])$(id).disabled=false;
    $('clearDialog').close();
    clearing=false;
    await switchView('tracks');await status();await loadSources();
    toast(result.warning||'Библиотека очищена. Музыкальные файлы остались на диске.',!!result.warning);
  }finally{
    clearing=false;
    for(const id of ['confirmClear','cancelClear','clearLibrary'])$(id).disabled=false;
    $('clearProgress').classList.add('hidden');
    updateSourceState();
  }
});
async function loadSources(){
  const revision=++sourcesRevision;
  const result=await api('/api/sources');
  if(revision!==sourcesRevision)return;
  sources=result;
  const list=$('sourceList');list.replaceChildren();
  $('sourcesEmpty').classList.toggle('hidden',sources.length>0);
  for(const source of sources){
    const card=el('article','source-card'),header=el('div','source-heading'),actions=el('div','source-actions');
    card.dataset.sourcePath=source.path;
    const name=el('strong','',source.name);name.title=source.path;
    const start=el('button','text-button','↻');start.type='button';start.dataset.sourceScan='';
    start.title=`Сканировать: ${source.path}`;start.setAttribute('aria-label',start.title);
    start.onclick=safe(()=>scan(source.path));
    const remove=el('button','text-button','×');remove.type='button';remove.dataset.sourceRemove='';
    remove.title=`Убрать из списка: ${source.path}`;remove.setAttribute('aria-label',remove.title);
    remove.onclick=safe(async()=>{
      remove.disabled=true;
      try{
        await api('/api/sources/'+source.id,{method:'DELETE'});await loadSources();
        toast('Папка убрана из списка источников. Треки остались в библиотеке.');
      }finally{remove.disabled=false}
    });
    actions.append(start,remove);header.append(name,actions);
    const path=el('div','source-path',source.path);path.title=source.path;
    card.append(header,path);
    if(source.last_scan_at){const last=el('div','source-last',`Запуск: ${new Date(source.last_scan_at).toLocaleString('ru-RU')}`);card.append(last)}
    list.append(card);
  }
  updateSourceState();
}
function updateSourceState(){
  const blocked=scanRunning||scanStarting||clearing;
  for(const id of ['chooseFolder','rescan','scanPath','emptyChoose'])$(id).disabled=blocked;
  for(const card of $('sourceList').children){
    const active=scanRunning&&card.dataset.sourcePath===activeSource;
    card.classList.toggle('scanning',active);
    card.querySelector('[data-source-scan]').disabled=blocked;
    card.querySelector('[data-source-remove]').disabled=active||scanStarting||clearing;
  }
}
let settingsSaving=false;
$('scanSettings').onclick=safe(async()=>{
  const settings=await api('/api/settings');
  $('automaticWorkers').checked=settings.scan_workers===0;
  $('workerCount').value=settings.scan_workers||8;
  $('workerCount').disabled=$('automaticWorkers').checked;
  $('settingsDialog').showModal();
});
$('automaticWorkers').onchange=()=>{$('workerCount').disabled=$('automaticWorkers').checked};
function closeSettings(){if(!settingsSaving)$('settingsDialog').close()}
$('closeSettings').onclick=closeSettings;$('cancelSettings').onclick=closeSettings;
$('settingsDialog').addEventListener('cancel',event=>{if(settingsSaving)event.preventDefault()});
$('scanSettingsForm').onsubmit=event=>{
  event.preventDefault();
  safe(async()=>{
    if(settingsSaving)return;
    const workers=$('automaticWorkers').checked?0:Number($('workerCount').value);
    if(!Number.isInteger(workers)||workers<0||workers>16||(!$('automaticWorkers').checked&&workers===0))throw Error('Выберите от 1 до 16 потоков.');
    settingsSaving=true;
    for(const id of ['saveScanSettings','cancelSettings','closeSettings','automaticWorkers','workerCount'])$(id).disabled=true;
    try{
      await api('/api/settings',{method:'PUT',body:JSON.stringify({scan_workers:workers})});
      $('settingsDialog').close();
      toast(workers?`Сохранено: ${workers} потоков. Применится к следующему сканированию или массовой записи.`:'Сохранён автоматический выбор числа потоков.');
    }finally{
      settingsSaving=false;
      for(const id of ['saveScanSettings','cancelSettings','closeSettings','automaticWorkers'])$(id).disabled=false;
      $('workerCount').disabled=$('automaticWorkers').checked;
    }
  })();
};
let filterTimer;for(const id of ['search','artistFilter','albumFilter'])$(id).oninput=()=>{clearTimeout(filterTimer);filterTimer=setTimeout(safe(async()=>{offset=0;await loadTracks()}),200)};
$('sort').onchange=safe(async()=>{offset=0;await loadTracks()});$('direction').onclick=safe(async()=>{order=order==='asc'?'desc':'asc';$('direction').textContent=order==='asc'?'↑':'↓';const label=order==='asc'?'Сортировать по убыванию':'Сортировать по возрастанию';$('direction').title=label;$('direction').setAttribute('aria-label',label);offset=0;await loadTracks()});
async function resetFilters(){['search','artistFilter','albumFilter','coverFilter'].forEach(id=>$(id).value='');offset=0;await loadTracks()}
$('resetFilters').onclick=safe(resetFilters);$('allTracks').onclick=safe(async()=>{await resetFilters();await switchView('tracks')});$('prev').onclick=safe(async()=>{offset=Math.max(0,offset-100);await loadTracks()});$('next').onclick=safe(async()=>{offset+=100;await loadTracks()});
$('lookup').onclick=()=>{$('sourceTitle').value=form.elements.title.value||selected.name.replace(/\.[^.]+$/,'');$('sourceArtist').value=form.elements.artist.value;$('candidates').replaceChildren();$('searchDialog').showModal()};
let searchRevision=0,searchController=null;
function cancelSourceSearch(){searchRevision++;searchController?.abort();searchController=null}
$('closeSearch').onclick=()=>$('searchDialog').close();
$('searchDialog').addEventListener('close',cancelSourceSearch);
$('sourceSearch').onsubmit=async e=>{
  e.preventDefault();
  cancelSourceSearch();
  const revision=searchRevision;
  searchController=new AbortController();
  $('candidates').textContent='Ищем совпадения и проверяем обложки…';
  try{
    const q=new URLSearchParams({title:$('sourceTitle').value,artist:$('sourceArtist').value});
    const candidates=await api('/api/search?'+q,{signal:searchController.signal});
    if(revision!==searchRevision)return;
    $('candidates').replaceChildren();
    if(!candidates.length){$('candidates').textContent='Совпадений нет. Попробуйте другое название.';return}
    for(const c of candidates){
      const row=el('div','candidate'),text=el('div');
      text.append(el('strong','',c.tags.title),el('p','',`${c.tags.artist} · ${c.tags.album||'Издание не указано'}`),el('small','',`${c.tags.date||'Без даты'} ${c.country} · ${duration(c.length/1000)} · Релевантность ${c.score}/100`));
      const artStatus=el('span','cover-status'+(c.cover_status==='available'?' available':''),c.cover_status==='available'?'Есть обложка':c.cover_status==='missing'?'Без обложки':'Обложка: не удалось проверить');
      text.append(artStatus);
      const apply=el('button','secondary','Применить');
      apply.onclick=safe(async()=>{
        document.querySelectorAll('.candidate button').forEach(b=>b.disabled=true);
        apply.textContent='Загрузка…';
        try{await applyCandidate(c)}finally{
          document.querySelectorAll('.candidate button').forEach(b=>b.disabled=false);
          apply.textContent='Применить';
        }
      });
      row.append(text,apply);$('candidates').append(row);
    }
  }catch(error){if(revision===searchRevision)$('candidates').textContent=error.message}
};
async function applyCandidate(c){const id=selected.id;let art=null,artError='';if($('fetchArt').checked&&c.release_id&&c.cover_status!=='missing'&&selected.cover_writable){try{const res=await fetch('/api/artwork/'+c.release_id);if(!res.ok){const data=await res.json();throw Error(data.error)}const blob=await res.blob();art=await new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(reader.result);reader.onerror=()=>reject(Error('Не удалось прочитать обложку'));reader.readAsDataURL(blob)})}catch(e){artError=e.message}}if(selected.id!==id)return;for(const k of fields){if(c.tags[k])form.elements[k].value=c.tags[k]}if(art){coverAction='replace';coverData=art.split(',')[1];showCover(art)}refreshEditorChanges();$('searchDialog').close();toast(artError?'Теги применены. Обложка: '+artError:'Данные добавлены в редактор. Проверьте и сохраните изменения.',!!artError)}
$('about').onclick=()=>$('infoDialog').showModal();$('closeInfo').onclick=()=>$('infoDialog').close();$('keepEditing').onclick=()=>$('discardDialog').close();$('confirmDiscard').onclick=safe(async()=>{$('discardDialog').close();await selectTrack(pendingSelection)});
document.addEventListener('keydown',e=>{if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='k'){e.preventDefault();$(view==='skipped'?'skippedSearch':'search').focus()}if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='s'){e.preventDefault();if(view==='tracks')safe(save)()}});
window.addEventListener('beforeunload',e=>{if(dirty){e.preventDefault();e.returnValue=''}});
async function poll(){try{await status()}catch(e){$('collectionSubtitle').textContent='Нет связи с приложением. '+e.message}setTimeout(poll,1200)}
function countLabel(n,one,few,many){const v=n%100;return `${n} ${v>=11&&v<=14?many:n%10===1?one:n%10>=2&&n%10<=4?few:many}`}
function updateNavigation(){
  const missing=view==='tracks'&&$('coverFilter').value==='missing';
  $('allTracks').classList.toggle('active',view==='tracks'&&!missing);
  $('withoutCover').classList.toggle('active',missing);
  $('skippedTab').classList.toggle('active',view==='skipped');
  for(const id of ['allTracks','withoutCover','skippedTab']){
    if($(id).classList.contains('active'))$(id).setAttribute('aria-current','page');else $(id).removeAttribute('aria-current');
  }
}
async function switchView(next){
  view=next;
  for(const id of ['trackToolbar','trackFilters','trackWorkspace'])$(id).classList.toggle('hidden',view==='skipped');
  $('skippedWorkspace').classList.toggle('hidden',view!=='skipped');
  $('viewTitle').textContent=view==='skipped'?'Пропущенные файлы':'Ваша коллекция';
  $('collectionSubtitle').textContent=view==='skipped'?'Что не попало в библиотеку и почему.':'Каждый трек на своём месте.';
  updateNavigation();await loadCurrentView();
}
async function loadCurrentView(){if(view==='skipped')await loadSkipped();else await loadTracks()}
async function loadSkipped(){
  const revision=++skippedRevision;
  const q=new URLSearchParams({q:$('skippedSearch').value,kind:$('skippedKind').value,offset:skippedOffset});
  const page=await api('/api/skipped?'+q);
  if(revision!==skippedRevision)return;
  skippedTotal=page.total;
  if(skippedTotal>0&&skippedOffset>=skippedTotal){skippedOffset=Math.floor((skippedTotal-1)/100)*100;return loadSkipped()}
  const container=$('skippedFiles');container.replaceChildren();
  const kinds={read_error:'Ошибка чтения',import_error:'Ошибка импорта',not_audio:'Нет аудио',excluded:'Исключено'};
  for(const file of page.files){
    const row=el('article','skipped-file'),heading=el('div','skipped-heading');
    heading.append(el('strong','',file.name),el('span','skip-badge '+file.kind,kinds[file.kind]||file.kind));
    row.append(heading,el('p','skipped-path',file.path),el('p','skipped-reason',file.reason));
    const details=el('details','skipped-details');
    details.append(el('summary','','Подробности'),el('pre','',file.detail),el('p','',`Папка: ${file.root}`),el('p','',`Проверено: ${new Date(file.scanned_at).toLocaleString('ru-RU')}`));
    row.append(details);container.append(row);
  }
  $('skippedTotal').textContent=countLabel(skippedTotal,'файл','файла','файлов');
  $('skippedEmpty').classList.toggle('hidden',skippedTotal>0);container.classList.toggle('hidden',skippedTotal===0);
  const filtered=$('skippedSearch').value||$('skippedKind').value;
  $('skippedEmpty').querySelector('h2').textContent=filtered?'Ничего не найдено':'Пропущенных файлов нет';
  $('skippedEmpty').querySelector('p').textContent=filtered?'Измените поисковый запрос или причину пропуска.':'После сканирования здесь появятся файлы, которые не удалось импортировать, и причины пропуска.';
  $('skippedPageInfo').textContent=skippedTotal?`${skippedOffset+1}–${Math.min(skippedOffset+100,skippedTotal)} из ${skippedTotal}`:'';
  $('skippedPrev').disabled=skippedOffset===0;$('skippedNext').disabled=skippedOffset+100>=skippedTotal;
}
$('withoutCover').onclick=safe(async()=>{$('coverFilter').value='missing';offset=0;await switchView('tracks')});
$('skippedTab').onclick=safe(()=>switchView('skipped'));
$('coverFilter').onchange=safe(async()=>{offset=0;await loadTracks()});
let skippedFilterTimer;
$('skippedSearch').oninput=()=>{clearTimeout(skippedFilterTimer);skippedFilterTimer=setTimeout(safe(async()=>{skippedOffset=0;await loadSkipped()}),200)};
$('skippedKind').onchange=safe(async()=>{skippedOffset=0;await loadSkipped()});
$('skippedPrev').onclick=safe(async()=>{skippedOffset=Math.max(0,skippedOffset-100);await loadSkipped()});
$('skippedNext').onclick=safe(async()=>{skippedOffset+=100;await loadSkipped()});
function applyTheme(){
  document.documentElement.dataset.theme=themePreference==='system'?(systemTheme.matches?'dark':'light'):themePreference;
  document.querySelectorAll('[data-theme-choice]').forEach(button=>button.setAttribute('aria-pressed',String(button.dataset.themeChoice===themePreference)));
}
systemTheme.addEventListener('change',applyTheme);
for(const button of document.querySelectorAll('[data-theme-choice]'))button.onclick=safe(async()=>{
  const previous=themePreference;themePreference=button.dataset.themeChoice;applyTheme();
  document.querySelectorAll('[data-theme-choice]').forEach(b=>b.disabled=true);
  try{await api('/api/settings',{method:'PUT',body:JSON.stringify({theme:themePreference})})}
  catch(error){themePreference=previous;applyTheme();throw error}
  finally{document.querySelectorAll('[data-theme-choice]').forEach(b=>b.disabled=false)}
});
async function initialize(){
  const settings=await api('/api/settings');themePreference=settings.theme;applyTheme();
  await Promise.all([loadTracks(),loadSources()]);
}
applyTheme();safe(initialize)();poll();
