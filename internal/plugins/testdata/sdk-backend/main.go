// This fixture is a separate process using the public Grafana SDK protocol.
package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type fixture struct{}

const appModule = `System.register(["react","@grafana/data","@grafana/runtime","react-router-dom"],function(_export){let React,data,runtime,router;return{setters:[m=>React=m,m=>data=m,m=>runtime=m,m=>router=m],execute:function(){
let initCalls=0;
function Root(props){const [resource,setResource]=React.useState(null);const location=router.useLocation();React.useEffect(()=>{let active=true;runtime.getBackendSrv().get('/api/plugins/'+props.meta.id+'/resources/example').then(value=>{if(active)setResource(value)});return()=>{active=false}},[props.meta.version]);return React.createElement(runtime.PluginPage,{pageNav:{text:'SDK route'},subTitle:'Original AppPlugin React component',actions:React.createElement(router.Link,{to:props.basename+'/details'},'Open SDK details')},React.createElement('p',null,'Current path: '+location.pathname),React.createElement('p',null,'Init calls: '+initCalls),React.createElement('p',null,'App label: '+(props.meta.jsonData.label||'')),React.createElement('p',null,'Backend configured: '+(resource?.configured?'yes':'no')),React.createElement('p',null,'Backend label: '+(resource?.label||''))) }
function Config({plugin}){const [label,setLabel]=React.useState(plugin.meta.jsonData.label||'');const [saved,setSaved]=React.useState(false);return React.createElement('form',{onSubmit:async event=>{event.preventDefault();await runtime.getBackendSrv().post('/api/plugins/'+plugin.meta.id+'/settings',{version:plugin.meta.version,jsonData:{label}});setSaved(true)}},React.createElement('label',null,'SDK label',React.createElement('input',{value:label,onChange:event=>setLabel(event.target.value)})),React.createElement('button',{type:'submit'},'Save SDK configuration'),saved&&React.createElement('p',{role:'status'},'SDK settings saved'))}
const plugin=new data.AppPlugin().setRootPage(Root).addConfigPage({id:'configuration',title:'SDK settings',body:Config});plugin.init=async function(){initCalls++};_export('plugin',plugin)
}}})`

var liveStarted, liveActive, liveCancelled, staticQueries atomic.Int64

func (fixture) QueryData(ctx context.Context, r *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	out := backend.NewQueryDataResponse()
	for _, q := range r.Queries {
		var input struct {
			Value float64 `json:"value"`
			Fail  bool    `json:"fail"`
			Live  bool    `json:"live"`
		}
		if err := json.Unmarshal(q.JSON, &input); err != nil {
			return nil, err
		}
		if input.Fail {
			out.Responses[q.RefID] = backend.DataResponse{Error: fmt.Errorf("fixture query failed"), Status: backend.StatusBadRequest}
			continue
		}
		settings := r.PluginContext.DataSourceInstanceSettings
		if settings == nil || settings.DecryptedSecureJSONData["apiKey"] != "test-secret-233" {
			return nil, fmt.Errorf("decrypted datasource secret missing")
		}
		frame := data.NewFrame("sdk-fixture", data.NewField("Time", nil, []time.Time{q.TimeRange.To}), data.NewField("Value", data.Labels{"source": settings.UID}, []float64{input.Value}))
		frame.RefID = q.RefID
		if input.Live {
			frame.Meta = &data.FrameMeta{Channel: "ds/" + settings.UID + "/counter"}
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
	return sender.Send(&backend.CallResourceResponse{Status: 200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: body})
}
func main() {
	if len(os.Args) == 3 && (os.Args[1] == "--package" || os.Args[1] == "--package-app") {
		if err := packageVariant(os.Args[2], os.Args[1] == "--package-app"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	f := fixture{}
	if err := backend.Manage("metricspanel-sdk-datasource", backend.ServeOpts{QueryDataHandler: f, CheckHealthHandler: f, CallResourceHandler: f, StreamHandler: f}); err != nil {
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

func packageVariant(destination string, app bool) error {
	name := "fixture_" + runtime.GOOS + "_" + runtime.GOARCH
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
	module := `System.register(["@grafana/data","@grafana/runtime"],function(_export){let data,runtime;return{setters:[function(m){data=m},function(m){runtime=m}],execute:function(){class Fixture extends runtime.DataSourceWithBackend{constructor(settings){super(settings);this.uid=settings.uid}query(request){if(request.targets.some(t=>t.directLive)){return runtime.getGrafanaLiveSrv().getDataStream({addr:{scope:"ds",namespace:this.uid,path:"counter"},filter:{fields:["Value"]},buffer:{maxLength:3}})}return super.query(request)}};_export("plugin",new data.DataSourcePlugin(Fixture))}}})`
	files := map[string][]byte{"plugin.json": []byte(`{"id":"metricspanel-sdk-datasource","name":"SDK Fixture","type":"datasource","backend":true,"executable":"fixture","info":{"version":"1.0.0"},"dependencies":{"grafanaDependency":">=12"}}`), "module.js": []byte(module), name: binary}
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
