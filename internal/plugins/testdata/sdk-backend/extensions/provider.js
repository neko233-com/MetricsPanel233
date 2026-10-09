System.register(["react","@grafana/data"],function(_export){let React,data;return{setters:[m=>React=m,m=>data=m],execute:function(){
const ID="__PROVIDER_ID__",VERSION="__VERSION__",POINT="metricspanel-ext-consumer-app/actions/v1";
let initCalls=0;
function Badge({label="badge"}){const context=data.usePluginContext();const [count,setCount]=React.useState(0);return React.createElement('div',{role:'group','aria-label':ID+' '+label},React.createElement('p',null,'Provider '+label+': '+context.meta.id+' / '+(context.meta.jsonData?.label||'unset')+' / v'+VERSION),React.createElement('button',{onClick:()=>setCount(count+1)},'Badge clicks: '+count))}
function Root(){return React.createElement('p',null,'Provider root v'+VERSION+', init calls '+initCalls)}
const plugin=new data.AppPlugin().setRootPage(Root);
plugin.init=()=>{initCalls++};
plugin.exposeComponent({id:ID+'/badge/v1',title:'SDK badge',description:'Provider plugin context',component:Badge});
plugin.addComponent({targets:POINT,title:'Added badge',description:'First added component',component:Badge});
plugin.addComponent({targets:POINT,title:'Second added badge',description:'Per-plugin component limit',component:Badge});
plugin.addComponent({targets:data.PluginExtensionPoints.ExtensionSidebar,title:'SDK details',description:'Provider sidebar',component:Badge});
plugin.addComponent({targets:data.PluginExtensionPoints.AppChrome,title:'SDK chrome',description:'Core app chrome',component:()=>React.createElement('span',null,'SDK chrome v'+VERSION)});
plugin.addFunction({targets:POINT,title:'Increment',description:'Synchronous extension function',fn:value=>value+1});
plugin.addFunction({targets:POINT,title:'Second function',description:'Per-plugin function limit',fn:async value=>value+1000});
plugin.addLink({targets:POINT,title:'Hidden first link',description:'Hidden links do not consume the limit',path:'/a/'+ID+'/',configure:()=>undefined});
plugin.addLink({targets:POINT,title:'Invalid async configure',description:'Rejected asynchronous configure must be hidden safely',path:'/a/'+ID+'/',configure:()=>Promise.reject(new Error('Invalid async configure'))});
plugin.addLink({targets:POINT,title:'Open provider',description:'Context-driven link',path:'/a/'+ID+'/details',configure:context=>{
if(context?.hide)return undefined;
const blocked=Reflect.set(context.nested,'value','changed')===false;
return{title:'Open '+context.nested.value+' / readonly '+blocked,path:'/a/'+ID+'/details?value='+context.nested.value,onClick:()=>{throw new Error('Restricted configure property executed')}}
}});
plugin.addLink({targets:POINT,title:'Second visible link',description:'Per-plugin link limit',path:'/a/'+ID+'/second'});
plugin.addLink({targets:POINT,title:'Invalid external link',description:'Forbidden path',path:'https://example.com/'});
plugin.addLink({targets:data.PluginExtensionPoints.SingleTopBarAction,title:'SDK sidebar',description:'Toggle registered sidebar',onClick:(_event,helpers)=>helpers.toggleSidebar('SDK details',{label:'sidebar'})});
plugin.addLink({targets:data.PluginExtensionPoints.DashboardPanelMenu,title:'Inspect SDK panel',description:'Panel data and modal context',group:{name:'SDK actions'},onClick:(_event,helpers)=>{
const context=helpers.context;
helpers.openModal({title:'SDK panel details',width:640,body:({onDismiss})=>React.createElement('div',null,
React.createElement(Badge,{label:'modal'}),
React.createElement('p',null,'Panel context: '+context.id+' / '+context.title+' / '+context.dashboard.uid+' / '+context.pluginId),
React.createElement('p',null,'Dashboard tags: '+context.dashboard.tags.join(',')),
React.createElement('p',null,'Query refs: '+context.targets.map(query=>query.refId).join(',')),
React.createElement('p',null,'Data frames: '+context.data.series.length),
React.createElement('p',null,'Time range: '+context.timeRange.from+' / '+context.timeRange.to),
React.createElement('button',{onClick:onDismiss},'Dismiss SDK modal'))})
}});
_export('plugin',plugin)
}}})
