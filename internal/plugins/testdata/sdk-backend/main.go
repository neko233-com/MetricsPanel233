// This fixture is a separate process using the public Grafana SDK protocol.
package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fixture struct{}

const appModule = `System.register(["react","@grafana/data","@grafana/runtime","react-router-dom"],function(_export){let React,data,runtime,router;return{setters:[m=>React=m,m=>data=m,m=>runtime=m,m=>router=m],execute:function(){
let initCalls=0;
function Root(props){const [resource,setResource]=React.useState(null);const location=router.useLocation();React.useEffect(()=>{let active=true;runtime.getBackendSrv().get('/api/plugins/'+props.meta.id+'/resources/example').then(value=>{if(active)setResource(value)});return()=>{active=false}},[props.meta.version]);return React.createElement(runtime.PluginPage,{pageNav:{text:'SDK route'},subTitle:'Original AppPlugin React component',actions:React.createElement(router.Link,{to:props.basename+'/details'},'Open SDK details')},React.createElement('p',null,'Current path: '+location.pathname),React.createElement('p',null,'Init calls: '+initCalls),React.createElement('p',null,'App label: '+(props.meta.jsonData.label||'')),React.createElement('p',null,'Backend configured: '+(resource?.configured?'yes':'no')),React.createElement('p',null,'Backend label: '+(resource?.label||''))) }
function Config({plugin}){const [label,setLabel]=React.useState(plugin.meta.jsonData.label||'');const [saved,setSaved]=React.useState(false);return React.createElement('form',{onSubmit:async event=>{event.preventDefault();await runtime.getBackendSrv().post('/api/plugins/'+plugin.meta.id+'/settings',{version:plugin.meta.version,jsonData:{label}});setSaved(true)}},React.createElement('label',null,'SDK label',React.createElement('input',{value:label,onChange:event=>setLabel(event.target.value)})),React.createElement('button',{type:'submit'},'Save SDK configuration'),saved&&React.createElement('p',{role:'status'},'SDK settings saved'))}
const plugin=new data.AppPlugin().setRootPage(Root).addConfigPage({id:'configuration',title:'SDK settings',body:Config});plugin.init=async function(){initCalls++};_export('plugin',plugin)
}}})`

// This external plugin intentionally uses only the public Grafana SDK imports.
// It exercises BackendSrv.chunked, including schema-once DataFrame appends.
const datasourceModule = `System.register(["@grafana/data","@grafana/runtime","rxjs","react"],function(_export){let data,runtime,rx,React;return{setters:[m=>data=m,m=>runtime=m,m=>rx=m,m=>React=m],execute:function(){
class Fixture extends runtime.DataSourceWithBackend {
constructor(settings){super(settings);this.uid=settings.uid}
query(request){
if(request.targets.some(t=>t.directLive)){return runtime.getGrafanaLiveSrv().getDataStream({addr:{scope:"ds",namespace:this.uid,path:"counter"},filter:{fields:["Value"]},buffer:{maxLength:3}})}
if(!request.targets.some(t=>t.directChunks)){return super.query(request)}
return new rx.Observable(observer=>{
const frames=new Map(),errors=new Map(),decoder=new TextDecoder('utf-8',{fatal:true});let pending='',total=0;
const emit=state=>observer.next({data:Array.from(frames.values(),entry=>entry.frame),state,error:errors.size?{message:Array.from(errors,([ref,message])=>ref+': '+message).join('; ')}:undefined});
const line=value=>{
if(!value)return;const chunk=JSON.parse(value);if(chunk.error)errors.set(chunk.refId,chunk.error);
if(chunk.frame){const key=JSON.stringify([chunk.refId,chunk.frameId]),previous=frames.get(key),schema=chunk.frame.schema||previous?.schema;if(!schema)throw new Error('Missing initial chunk schema');const next=data.dataFrameFromJSON({schema,data:chunk.frame.data});next.refId=chunk.refId;
if(previous){next.fields=next.fields.map((field,index)=>({...field,values:previous.frame.fields[index].values.concat(field.values)}));next.length=previous.frame.length+next.length}
if(next.length>10000)throw new Error('Fixture frame row limit exceeded');frames.set(key,{schema,frame:next})}
emit(data.LoadingState.Streaming)
};
const consume=bytes=>{pending+=decoder.decode(bytes,{stream:true});let boundary;while((boundary=pending.indexOf('\n'))>=0){line(pending.slice(0,boundary));pending=pending.slice(boundary+1)}if(pending.length>8*1024*1024)throw new Error('Chunk line limit exceeded')};
const listener=runtime.getBackendSrv().chunked({url:'/apis/'+this.type+'.datasource.grafana.app/v0alpha1/namespaces/default/connections/'+this.uid+'/query',method:'POST',headers:{Accept:'text/jsonl'},data:{from:String(request.range.from.valueOf()),to:String(request.range.to.valueOf()),queries:request.targets.map(target=>({...target,intervalMs:request.intervalMs,maxDataPoints:request.maxDataPoints}))}}).subscribe({next:response=>{try{if(response.status>=300)throw new Error('Query HTTP '+response.status);if(response.data){total+=response.data.byteLength;if(total>32*1024*1024)throw new Error('Query byte limit exceeded');if(request.targets.some(t=>t.fragmentUTF8)){for(const byte of response.data)consume(new Uint8Array([byte]))}else consume(response.data)}else{pending+=decoder.decode();if(pending.trim())throw new Error('Truncated query line');emit(data.LoadingState.Done)}}catch(error){observer.error(error)}},error:error=>observer.error(error),complete:()=>observer.complete()});
return()=>listener.unsubscribe()
})
}}
function QueryEditor(props){const settings=data.usePluginContext().instanceSettings;return React.createElement('div',{'aria-label':'SDK metrics query editor'},
React.createElement('p',null,'SDK editor context: '+settings.uid+' / '+props.app+' / '+props.query.refId+' / '+(props.queries?.length||0)),
React.createElement('label',null,'SDK numeric value',React.createElement('input',{type:'number',value:props.query.value??233,onChange:e=>props.onChange({...props.query,value:Number(e.target.value)})})),
React.createElement('button',{type:'button',onClick:props.onRunQuery},'Run SDK metric query'),
React.createElement('button',{type:'button',disabled:!props.onAddQuery,onClick:()=>props.onAddQuery?.({...props.query,value:2})},'Add SDK metric query'))}
_export('plugin',new data.DataSourcePlugin(Fixture).setQueryEditor(QueryEditor))
}}})`

var liveStarted, liveActive, liveCancelled, staticQueries atomic.Int64
var chunkedStarted, chunkedActive, chunkedCancelled atomic.Int64
var annotationStarted, annotationActive, annotationCancelled atomic.Int64
var chunkedRefs sync.Map

func (fixture) QueryChunkedData(ctx context.Context, r *backend.QueryChunkedDataRequest, writer backend.ChunkedDataWriter) error {
	// Older Data servers return this gRPC status for the absent streaming RPC.
	if strings.Contains(filepath.Base(os.Args[0]), "_legacy_") {
		return status.Error(codes.Unimplemented, "legacy fixture has no chunked RPC")
	}
	chunkedStarted.Add(1)
	chunkedActive.Add(1)
	defer func() {
		chunkedActive.Add(-1)
		if ctx.Err() != nil {
			chunkedCancelled.Add(1)
		}
	}()
	for _, query := range r.Queries {
		counter, _ := chunkedRefs.LoadOrStore(query.RefID, &atomic.Int64{})
		counter.(*atomic.Int64).Add(1)
		var input struct {
			Value                   float64 `json:"value"`
			Chunks                  int     `json:"chunks"`
			DelayMS                 int     `json:"delayMs"`
			FirstDelayMS            int     `json:"firstDelayMs"`
			RequireApp              bool    `json:"requireApp"`
			UnknownRef              bool    `json:"unknownRef"`
			Fail                    bool    `json:"fail"`
			Secondary               bool    `json:"secondary"`
			UnimplementedAfterChunk bool    `json:"unimplementedAfterChunk"`
		}
		if err := json.Unmarshal(query.JSON, &input); err != nil {
			return err
		}
		if input.Fail {
			if err := writer.WriteError(ctx, query.RefID, backend.StatusBadRequest, fmt.Errorf("fixture chunked query failed")); err != nil {
				return err
			}
			continue
		}
		settings := r.PluginContext.DataSourceInstanceSettings
		if settings == nil || settings.DecryptedSecureJSONData["apiKey"] != "test-secret-233" {
			return fmt.Errorf("decrypted datasource secret missing")
		}
		if input.RequireApp && (r.PluginContext.AppInstanceSettings == nil || r.PluginContext.AppInstanceSettings.DecryptedSecureJSONData["apiKey"] != "app-secret-233") {
			return fmt.Errorf("decrypted app secret missing")
		}
		if input.Chunks <= 0 {
			input.Chunks = 3
		}
		if input.Chunks > 1000 || input.DelayMS < 0 || input.DelayMS > 3000 || input.FirstDelayMS < 0 || input.FirstDelayMS > 3000 {
			return fmt.Errorf("invalid fixture chunk count/delay")
		}
		for index := 0; index < input.Chunks; index++ {
			delay := input.DelayMS
			if index == 0 {
				delay = input.FirstDelayMS
			}
			if delay > 0 {
				select {
				case <-time.After(time.Duration(delay) * time.Millisecond):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			frame := data.NewFrame("sdk-chunked", data.NewField("Time", nil, []time.Time{query.TimeRange.To.Add(time.Duration(index) * time.Second)}), data.NewField("Value", data.Labels{"source": settings.UID}, []float64{input.Value + float64(index)}), data.NewField("Region", nil, []string{"上海🌍"}))
			if input.UnknownRef {
				return writer.WriteFrame(ctx, "unknown", "primary", frame)
			}
			if err := writer.WriteFrame(ctx, query.RefID, "primary", frame); err != nil {
				return err
			}
			if input.Secondary {
				second := data.NewFrame("sdk-secondary", data.NewField("Value", nil, []float64{input.Value*2 + float64(index)}))
				if err := writer.WriteFrame(ctx, query.RefID, "secondary", second); err != nil {
					return err
				}
			}
			if input.UnimplementedAfterChunk {
				return status.Error(codes.Unimplemented, "fixture failed after first chunk")
			}
		}
	}
	return nil
}

func (fixture) QueryData(ctx context.Context, r *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	out := backend.NewQueryDataResponse()
	for _, q := range r.Queries {
		var input struct {
			Value             float64 `json:"value"`
			Fail              bool    `json:"fail"`
			Live              bool    `json:"live"`
			Annotation        bool    `json:"annotation"`
			AnnotationText    string  `json:"annotationText"`
			AnnotationDelayMS int     `json:"annotationDelayMS"`
			RequireAlert      bool    `json:"requireAlert"`
			SQLTable          bool    `json:"sqlTable"`
		}
		if err := json.Unmarshal(q.JSON, &input); err != nil {
			return nil, err
		}
		if input.Fail {
			out.Responses[q.RefID] = backend.DataResponse{Error: fmt.Errorf("fixture query failed"), Status: backend.StatusBadRequest}
			continue
		}
		settings := r.PluginContext.DataSourceInstanceSettings
		if input.RequireAlert && (r.Headers["FromAlert"] != "true" || r.Headers["X-Cache-Skip"] != "true" || r.Headers["X-Grafana-Org-Id"] != "1") {
			return nil, fmt.Errorf("Grafana alert request headers missing")
		}
		if settings == nil || settings.DecryptedSecureJSONData["apiKey"] != "test-secret-233" {
			return nil, fmt.Errorf("decrypted datasource secret missing")
		}
		if input.Annotation {
			annotationStarted.Add(1)
			annotationActive.Add(1)
			defer annotationActive.Add(-1)
			if input.AnnotationDelayMS < 0 || input.AnnotationDelayMS > 5000 {
				return nil, fmt.Errorf("invalid annotation fixture delay")
			}
			if input.AnnotationDelayMS > 0 {
				select {
				case <-time.After(time.Duration(input.AnnotationDelayMS) * time.Millisecond):
				case <-ctx.Done():
					annotationCancelled.Add(1)
					return nil, ctx.Err()
				}
			}
			at := q.TimeRange.From.Add(q.TimeRange.To.Sub(q.TimeRange.From) / 2)
			frame := data.NewFrame("sdk-annotation", data.NewField("When", nil, []time.Time{at}), data.NewField("End", nil, []time.Time{at.Add(time.Second)}), data.NewField("Detail", nil, []string{input.AnnotationText}), data.NewField("Tags", nil, []string{"service:api,plugin"}), data.NewField("EventKey", nil, []string{"1"}))
			frame.RefID = q.RefID
			out.Responses[q.RefID] = backend.DataResponse{Frames: data.Frames{frame}}
			continue
		}
		if input.SQLTable {
			frame := data.NewFrame("sdk-sql-table", data.NewField("host", nil, []string{settings.UID}), data.NewField("value", nil, []float64{input.Value}), data.NewField("budget", nil, []float64{input.Value * 2}), data.NewField("online", nil, []bool{true}), data.NewField("payload", nil, []json.RawMessage{json.RawMessage(`{"n":233}`)}), data.NewField("clock", nil, []time.Time{q.TimeRange.To}))
			frame.RefID = q.RefID
			frame.Meta = &data.FrameMeta{Type: data.FrameTypeTable}
			out.Responses[q.RefID] = backend.DataResponse{Frames: data.Frames{frame}}
			continue
		}
		frame := data.NewFrame("sdk-fixture", data.NewField("Time", nil, []time.Time{q.TimeRange.To}), data.NewField("Value", data.Labels{"source": settings.UID}, []float64{input.Value}))
		frame.RefID = q.RefID
		frame.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesMulti}
		if input.Live {
			frame.Meta.Channel = "ds/" + settings.UID + "/counter"
		} else {
			staticQueries.Add(1)
		}
		out.Responses[q.RefID] = backend.DataResponse{Frames: data.Frames{frame}}
	}
	return out, nil
}
func (fixture) CheckHealth(ctx context.Context, r *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	if os.Getenv("METRICSPANEL_TOKEN") != "" {
		return nil, fmt.Errorf("server token leaked to plugin process")
	}
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: "Grafana SDK backend is working", JSONDetails: json.RawMessage(`{"protocol":2}`)}, nil
}
func (fixture) CallResource(ctx context.Context, r *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	uid := ""
	if r.PluginContext.DataSourceInstanceSettings != nil {
		uid = r.PluginContext.DataSourceInstanceSettings.UID
	}
	body, _ := json.Marshal(map[string]any{"path": r.Path, "url": r.URL, "method": r.Method, "pid": os.Getpid(), "uid": uid})
	if settings := r.PluginContext.AppInstanceSettings; settings != nil {
		var values map[string]any
		_ = json.Unmarshal(settings.JSONData, &values)
		body, _ = json.Marshal(map[string]any{"plugin": r.PluginContext.PluginID, "org": r.PluginContext.OrgID, "configured": settings.DecryptedSecureJSONData["apiKey"] == "app-secret-233", "label": values["label"], "uid": uid})
	}
	if r.Path == "stream-stats" {
		body, _ = json.Marshal(map[string]int64{"started": liveStarted.Load(), "active": liveActive.Load(), "cancelled": liveCancelled.Load(), "static_queries": staticQueries.Load()})
	}
	if r.Path == "chunked-stats" {
		counters := map[string]int64{"started": chunkedStarted.Load(), "active": chunkedActive.Load(), "cancelled": chunkedCancelled.Load(), "static_queries": staticQueries.Load()}
		chunkedRefs.Range(func(ref, counter any) bool {
			counters["ref_"+ref.(string)] = counter.(*atomic.Int64).Load()
			return true
		})
		body, _ = json.Marshal(counters)
	}
	if r.Path == "annotation-stats" {
		body, _ = json.Marshal(map[string]int64{"started": annotationStarted.Load(), "active": annotationActive.Load(), "cancelled": annotationCancelled.Load()})
	}
	return sender.Send(&backend.CallResourceResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: body})
}
func main() {
	if len(os.Args) == 3 && strings.HasPrefix(os.Args[1], "--package-extension-") {
		if err := packageExtensions(os.Args[2], os.Args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 3 && (os.Args[1] == "--package" || os.Args[1] == "--package-app" || os.Args[1] == "--package-legacy" || os.Args[1] == "--package-annotations") {
		if err := packageVariant(os.Args[2], os.Args[1] == "--package-app", os.Args[1] == "--package-legacy", os.Args[1] == "--package-annotations"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	f := fixture{}
	opts := backend.ServeOpts{QueryDataHandler: f, QueryChunkedDataHandler: f, CheckHealthHandler: f, CallResourceHandler: f, StreamHandler: f}
	if err := backend.Manage("metricspanel-sdk-datasource", opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (fixture) SubscribeStream(ctx context.Context, r *backend.SubscribeStreamRequest) (*backend.SubscribeStreamResponse, error) {
	valid := r.PluginContext.DataSourceInstanceSettings != nil && r.PluginContext.DataSourceInstanceSettings.DecryptedSecureJSONData["apiKey"] == "test-secret-233"
	valid = valid || r.PluginContext.AppInstanceSettings != nil && r.PluginContext.AppInstanceSettings.DecryptedSecureJSONData["apiKey"] == "app-secret-233"
	if !valid {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusPermissionDenied}, nil
	}
	if r.Path == "denied" {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusPermissionDenied}, nil
	}
	if r.Path != "counter" {
		return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusNotFound}, nil
	}
	frame := data.NewFrame("live-counter", data.NewField("Time", nil, []time.Time{time.Now()}), data.NewField("Value", nil, []float64{233}))
	initial, err := backend.NewInitialFrame(frame, data.IncludeAll)
	return &backend.SubscribeStreamResponse{Status: backend.SubscribeStreamStatusOK, InitialData: initial}, err
}
func (fixture) PublishStream(ctx context.Context, r *backend.PublishStreamRequest) (*backend.PublishStreamResponse, error) {
	if r.Path != "counter" {
		return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusPermissionDenied}, nil
	}
	return &backend.PublishStreamResponse{Status: backend.PublishStreamStatusOK, Data: r.Data}, nil
}
func (fixture) RunStream(ctx context.Context, r *backend.RunStreamRequest, sender *backend.StreamSender) error {
	liveStarted.Add(1)
	liveActive.Add(1)
	defer func() { liveActive.Add(-1); liveCancelled.Add(1) }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	value := 233.0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case timestamp := <-ticker.C:
			value++
			frame := data.NewFrame("live-counter", data.NewField("Time", nil, []time.Time{timestamp}), data.NewField("Value", nil, []float64{value}))
			if err := sender.SendFrame(frame, data.IncludeAll); err != nil {
				return err
			}
		}
	}
}

func packageVariant(destination string, app, legacy, annotations bool) error {
	prefix := "fixture"
	if legacy {
		prefix += "_legacy"
	}
	name := prefix + "_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer file.Close()
	archive := zip.NewWriter(file)
	module := datasourceModule
	if annotations {
		contents, err := extensionFixtures.ReadFile("extensions/annotations-datasource.js")
		if err != nil {
			return err
		}
		module = string(contents)
	}
	files := map[string][]byte{"plugin.json": []byte(`{"id":"metricspanel-sdk-datasource","name":"SDK Fixture","type":"datasource","backend":true,"executable":"fixture","info":{"version":"1.0.0"},"dependencies":{"grafanaDependency":">=12"}}`), "module.js": []byte(module), name: binary}
	if annotations {
		files["plugin.json"] = []byte(strings.ReplaceAll(string(files["plugin.json"]), `"backend":true`, `"annotations":true,"backend":true`))
	}
	if legacy {
		files["plugin.json"] = []byte(strings.ReplaceAll(string(files["plugin.json"]), `"executable":"fixture"`, `"executable":"fixture_legacy"`))
	}
	if app {
		files["plugin.json"] = []byte(`{"id":"metricspanel-sdk-app","name":"SDK App Fixture","type":"app","backend":true,"executable":"fixture","info":{"version":"1.0.0"},"dependencies":{"grafanaDependency":">=12"}}`)
		files["child/plugin.json"] = []byte(`{"id":"metricspanel-sdk-datasource","name":"Bundled SDK datasource","type":"datasource","backend":true,"executable":"fixture","info":{"version":"1.0.0"},"dependencies":{"grafanaDependency":">=12"}}`)
		files["child/module.js"] = []byte(module)
		files["child/"+name] = binary
		files["module.js"] = []byte(appModule)
	}
	for name, body := range files {
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		if _, err = entry.Write(body); err != nil {
			return err
		}
	}
	return archive.Close()
}
