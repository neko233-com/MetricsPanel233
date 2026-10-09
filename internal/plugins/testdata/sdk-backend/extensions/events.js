System.register(["react","@grafana/data","@grafana/runtime"],function(_export){let React,data,runtime;return{setters:[m=>React=m,m=>data=m,m=>runtime=m],execute:function(){
class Ping extends data.BusEventWithPayload{static type='metricspanel-events-ping'}
function Observer(){
const [refreshes,setRefreshes]=React.useState(0),[ranges,setRanges]=React.useState(0),[raw,setRaw]=React.useState('unset'),[custom,setCustom]=React.useState(0),[legacy,setLegacy]=React.useState(0),[listening,setListening]=React.useState(true);
const bus=runtime.getAppEvents();
React.useEffect(()=>{
 const refresh=bus.getStream(runtime.RefreshEvent).subscribe(()=>setRefreshes(value=>value+1));
 const range=bus.subscribe(runtime.TimeRangeUpdatedEvent,event=>{setRanges(value=>value+1);setRaw(event.payload.raw.from+' / '+event.payload.raw.to+' / '+(event.payload.to.valueOf()-event.payload.from.valueOf()))});
 return()=>{refresh.unsubscribe();range.unsubscribe()}
},[bus]);
React.useEffect(()=>{if(!listening)return;const typed=bus.subscribe(Ping,event=>setCustom(value=>value+event.payload));const handler=value=>setLegacy(previous=>previous+value);bus.on('metricspanel-events-legacy',handler);return()=>{typed.unsubscribe();bus.off('metricspanel-events-legacy',handler)}},[bus,listening]);
return React.createElement('section',{'aria-label':'SDK global events'},
React.createElement('p',null,'App refreshes: '+refreshes),
React.createElement('p',null,'App ranges: '+ranges+' / '+raw),
React.createElement('p',null,'Custom events: '+custom+' / legacy: '+legacy),
React.createElement('button',{onClick:()=>bus.publish(new runtime.RefreshEvent())},'SDK global refresh'),
React.createElement('button',{onClick:()=>bus.emit('refresh')},'SDK legacy refresh'),
React.createElement('button',{onClick:()=>{for(let index=0;index<25;index++)bus.publish(new runtime.RefreshEvent())}},'SDK refresh burst'),
React.createElement('button',{onClick:()=>{bus.publish(new Ping(1));bus.emit('metricspanel-events-legacy',2)}},'Publish custom SDK event'),
React.createElement('button',{onClick:()=>setListening(!listening)},listening?'Stop custom events':'Resume custom events'),
['Success','Warning','Error','Info'].map(kind=>React.createElement('button',{key:kind,onClick:()=>bus.emit(data.AppEvents['alert'+kind],['SDK '+kind,kind==='Error'?new Error('233 failure'):'233 detail'])},'SDK '+kind+' notification'))
)
}
_export('plugin',new data.AppPlugin().setRootPage(Observer).addComponent({targets:data.PluginExtensionPoints.AppChrome,title:'Event observer',description:'Global SDK events',component:Observer}))
}}})
