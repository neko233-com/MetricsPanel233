System.register(["react","@grafana/data","@grafana/runtime"],function(_export){let React,data,runtime;return{setters:[m=>React=m,m=>data=m,m=>runtime=m],execute:function(){
function Panel({eventBus,data:frameData,timeRange,timeZone,onChangeTimeRange,replaceVariables}){
const [refreshes,setRefreshes]=React.useState(0),[ranges,setRanges]=React.useState(0);
React.useEffect(()=>{const refresh=eventBus.subscribe(runtime.RefreshEvent,()=>setRefreshes(value=>value+1));const range=eventBus.subscribe(runtime.TimeRangeUpdatedEvent,()=>setRanges(value=>value+1));return()=>{refresh.unsubscribe();range.unsubscribe()}},[eventBus]);
const field=frameData.series[0]?.fields.find(field=>field.type===data.FieldType.number);
return React.createElement('section',{'aria-label':'SDK panel events'},
React.createElement('p',null,'Panel refreshes: '+refreshes),
React.createElement('p',null,'Panel ranges: '+ranges),
React.createElement('p',null,'Panel value: '+(field?.values[0]??'empty')),
React.createElement('p',null,'Panel annotations: '+(frameData.annotations||[]).reduce((count,frame)=>count+frame.length,0)),
React.createElement('p',null,'Panel window: '+timeRange.from.valueOf()+' / '+timeRange.to.valueOf()+' / '+timeZone),
React.createElement('p',null,'Panel variables: '+replaceVariables('$__from / $__to / $__range_ms')),
React.createElement('details',null,React.createElement('summary',null,'Frame inspection'),React.createElement('pre',{style:{whiteSpace:'pre-wrap',overflowWrap:'anywhere',fontSize:'11px'}},'Frame details: '+JSON.stringify(frameData.series.map(frame=>({refId:frame.refId,compare:!!frame.meta?.timeCompare?.isTimeShiftQuery,diff:frame.meta?.timeCompare?.diffMs,time:frame.fields.find(field=>field.type===data.FieldType.time)?.values[0],value:frame.fields.find(field=>field.type===data.FieldType.number)?.values[0]}))))),
React.createElement('button',{onClick:()=>onChangeTimeRange({from:timeRange.from.valueOf()+20000,to:timeRange.to.valueOf()-20000})},'SDK zoom window'),
React.createElement('button',{onClick:()=>eventBus.publish(new runtime.RefreshEvent())},'SDK panel refresh'))
}
_export('plugin',new data.PanelPlugin(Panel))
}}})
