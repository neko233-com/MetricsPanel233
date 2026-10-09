System.register(["react","@grafana/data","@grafana/runtime"],function(_export){let React,data,runtime;return{setters:[m=>React=m,m=>data=m,m=>runtime=m],execute:function(){
const POINT="metricspanel-ext-consumer-app/actions/v1",PROVIDER="metricspanel-ext-provider-app";
function Root(){
const [hide,setHide]=React.useState(false),[observed,setObserved]=React.useState(-1),[observedComponents,setObservedComponents]=React.useState(-1),[retained,setRetained]=React.useState(null),[result,setResult]=React.useState('');
const context=React.useMemo(()=>({hide,nested:{value:'233'}}),[hide]);
const links=runtime.usePluginLinks({extensionPointId:POINT,context,limitPerPlugin:1});
const allLinks=runtime.usePluginLinks({extensionPointId:POINT,context});
const components=runtime.usePluginComponents({extensionPointId:POINT,limitPerPlugin:1});
const functions=runtime.usePluginFunctions({extensionPointId:POINT,limitPerPlugin:1});
const allFunctions=runtime.usePluginFunctions({extensionPointId:POINT});
const exposed=runtime.usePluginComponent(PROVIDER+'/badge/v1');
React.useEffect(()=>{const subscription=runtime.getObservablePluginLinks({extensionPointId:POINT,context:{nested:{value:'233'}},limitPerPlugin:1}).subscribe(values=>setObserved(values.length));return()=>subscription.unsubscribe()},[]);
React.useEffect(()=>{const subscription=runtime.getObservablePluginComponents({extensionPointId:POINT,limitPerPlugin:1}).subscribe(values=>setObservedComponents(values.length));return()=>subscription.unsubscribe()},[]);
return React.createElement('section',null,
React.createElement('p',null,'Link count: '+links.links.length),
React.createElement('p',null,'Unrestricted link count: '+allLinks.links.length),
React.createElement('p',null,'Observable link count: '+observed),
React.createElement('p',null,'Observable component count: '+observedComponents),
React.createElement('p',null,'Component count: '+components.components.length),
React.createElement('p',null,'Function count: '+functions.functions.length),
React.createElement('p',null,'Shared context value: '+context.nested.value),
React.createElement('button',{onClick:()=>setHide(!hide)},hide?'Show SDK links':'Hide SDK links'),
links.links.map(link=>React.createElement('a',{key:link.id,href:link.path},link.title)),
components.components.map(Component=>React.createElement(Component,{key:Component.meta.id,label:'added'})),
exposed.component&&React.createElement(exposed.component,{label:'exposed'}),
React.createElement('button',{onClick:()=>{const fn=functions.functions[0]?.fn;if(fn){setRetained(()=>fn);setResult(String(fn(233)))}}},'Run extension function'),
React.createElement('button',{onClick:async()=>{const fn=allFunctions.functions.find(item=>item.title==='Second function')?.fn;if(fn)setResult(String(await fn(233)))}},'Run async function'),
React.createElement('button',{onClick:()=>{try{setResult(String(retained?.(233)))}catch(error){setResult(error.message)}}},'Run retained function'),
React.createElement('p',null,'Function result: '+result)
)
}
_export('plugin',new data.AppPlugin().setRootPage(Root))
}}})
