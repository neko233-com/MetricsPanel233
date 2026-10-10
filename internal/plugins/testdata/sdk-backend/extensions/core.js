System.register(["react","@grafana/data","@grafana/runtime","@grafana/runtime/unstable","rxjs"],function(_export){let React,data,runtime,unstable,rx;return{setters:[m=>React=m,m=>data=m,m=>runtime=m,m=>unstable=m,m=>rx=m],execute:function(){
function Probe(){
const [result,setResult]=React.useState('idle'),[frames,setFrames]=React.useState('{}'),[files,setFiles]=React.useState('none');
const subscription=React.useRef(null);
React.useEffect(()=>()=>subscription.current?.unsubscribe(),[]);
const inspect=async()=>{try{
 await runtime.getDataSourceSrv().reload();
 const legacy=await runtime.getDataSourceSrv().get('-- Grafana --'),modern=await unstable.getDataSourceInstance({uid:'grafana'});
 const byType=await runtime.getDataSourceSrv().get({type:'grafana'}),byId=await runtime.getDataSourceSrv().get('-1');
 const defaults=await runtime.getDataSourceSrv().get();
 const now=Date.now(),range={from:data.dateTime(now-60000),to:data.dateTime(now+60000),raw:{from:'now-1m',to:'now+1m'}};
 const annotations=await rx.firstValueFrom(modern.query({requestId:'core-annotations',range,rangeRaw:range.raw,intervalMs:1000,targets:[{refId:'Anno',name:'Core API',queryType:'annotations',target:{type:'tags',tags:['core-sdk-233']}}]}));
 setResult(JSON.stringify({uid:modern.uid,id:modern.id,type:modern.type,same:legacy===modern&&byType===modern&&byId===modern,default:defaults.uid,
  annotations:annotations.data.map(frame=>({length:frame.length,fields:frame.fields.map(field=>({name:field.name,type:field.type,values:field.values}))})),
  query:modern.getDefaultQuery(),modernList:(await unstable.getDataSourceInstanceList({metrics:true})).map(s=>s.uid),legacyList:runtime.getDataSourceSrv().getList({metrics:true}).map(s=>s.uid),
  variableList:runtime.getDataSourceSrv().getList({variables:true}).map(s=>s.uid)}));
 const list=await rx.firstValueFrom(modern.listFiles('',20));setFiles(JSON.stringify(list.toArray()));
}catch(error){setResult('ERROR '+error.message)}};
const registered=async()=>{try{
 class Ephemeral extends runtime.RuntimeDataSource{constructor(uid){super('metricspanel-core-app',uid)}query(){return rx.of({data:[]})}}
 const one=new Ephemeral('core-legacy-runtime'),two=new Ephemeral('core-modern-runtime');
 runtime.getDataSourceSrv().registerRuntimeDataSource({dataSource:one});unstable.registerRuntimeDataSourceInstance({dataSource:two});
 await runtime.getDataSourceSrv().reload();
 const good=(await runtime.getDataSourceSrv().get(one.uid))===one&&(await unstable.getDataSourceInstance(one.uid))===one&&(await runtime.getDataSourceSrv().get(two.uid))===two&&(await unstable.getDataSourceInstance(two.uid))===two;
 let duplicate=false;try{runtime.getDataSourceSrv().registerRuntimeDataSource({dataSource:one})}catch{duplicate=true}
 setResult(JSON.stringify({runtime:good,duplicate}));
}catch(error){setResult('ERROR '+error.message)}};
const expression=async()=>{try{
 const source=await unstable.getDataSourceInstance('__expr__'),legacy=await runtime.getDataSourceSrv().get('-100'),named=await runtime.getDataSourceSrv().get('Expression');
 const settings=await unstable.getDataSourceInstanceSettings({type:'__expr__'}),range={from:data.dateTime(1000),to:data.dateTime(4000),raw:{from:'1000',to:'4000'}};
 const result=await rx.firstValueFrom(source.query({requestId:'expression-probe',range,rangeRaw:range.raw,intervalMs:1000,targets:[{refId:'A',hide:true,expr:'vector(233)',instant:true,datasource:{uid:'metricspanel'}},{refId:'C',type:'math',expression:'$A*2',datasource:{uid:'__expr__'}},{refId:'Bad',type:'math',expression:'$Missing',datasource:{uid:'__expr__'}}]}));
 let reserved=false;try{class Shadow extends runtime.RuntimeDataSource{constructor(){super('metricspanel-core-app','__expr__')}query(){return rx.of({data:[]})}}runtime.getDataSourceSrv().registerRuntimeDataSource({dataSource:new Shadow()})}catch{reserved=true}
 setResult(JSON.stringify({expression:true,same:source===legacy&&source===named,uid:settings.uid,readOnly:settings.readOnly,reserved,newQuery:source.newQuery({expression:'$A+1'}),state:result.state,error:result.error?.refId,frames:result.data.map(frame=>({refId:frame.refId,value:frame.fields.find(field=>field.type==='number')?.values[0]}))}));
}catch(error){setResult('ERROR '+error.message)}};
const live=async()=>{try{
 subscription.current?.unsubscribe();
 const source=await unstable.getDataSourceInstance('grafana'),range={from:data.dateTime(Date.now()-60000),to:data.dateTime(),raw:{from:'now-1m',to:'now'}};
 const chunks=new Map();
 subscription.current=source.query({requestId:'core-probe-live',range,rangeRaw:range.raw,intervalMs:1000,maxDataPoints:5,scopedVars:{source:{value:'core-live'}},targets:[{refId:'Static',queryType:'randomWalk',startValue:233,spread:0},{refId:'Stream',queryType:'measurements',channel:'ds/$source/counter',filter:{fields:['Time','Value']},buffer:15000}]}).subscribe({next:result=>{
 chunks.set(result.key,result);setFrames(JSON.stringify(Object.fromEntries(Array.from(chunks,([key,result])=>[key,{state:result.state,error:result.error?.message,frames:result.data.map(frame=>({refId:frame.refId,length:frame.length,fields:frame.fields.map(f=>f.name),value:frame.fields.find(f=>f.type==='number')?.values[frame.length-1]}))}]))));
},error:error=>setFrames('ERROR '+error.message)});
}catch(error){setFrames('ERROR '+error.message)}};
return React.createElement('section',{'aria-label':'Core SDK probe',style:{overflow:'auto',maxWidth:'100%'}},React.createElement('button',{onClick:inspect},'Inspect core source'),React.createElement('button',{onClick:registered},'Register runtime sources'),React.createElement('button',{onClick:expression},'Inspect expression SDK'),React.createElement('button',{onClick:live},'Start core measurements'),React.createElement('button',{onClick:()=>{subscription.current?.unsubscribe();subscription.current=null}},'Stop core measurements'),...['Core discovery: '+result,'Core files: '+files,'Core frames: '+frames].map((text,index)=>React.createElement('pre',{key:index,style:{whiteSpace:'pre-wrap',overflowWrap:'anywhere'}},text)))
}
_export('plugin',new data.PanelPlugin(Probe));
}}})
