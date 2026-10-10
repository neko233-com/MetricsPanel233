System.register(["@grafana/data", "@grafana/runtime", "rxjs", "react"], function (_export) {
  let data, runtime, rx, React;
  return { setters: [m => data = m, m => runtime = m, m => rx = m, m => React = m], execute() {
    const events = frames => frames.flatMap(frame => Array.from({ length: frame.length }, (_, row) => {
      const get = name => frame.fields.find(field => field.name === name)?.values[row];
      return { id: get("EventKey"), time: get("When"), timeEnd: get("End"), text: get("Detail"), tags: get("Tags")?.split(",") || [] };
    }));
    function QueryEditor(props) {
      const settings = data.usePluginContext().instanceSettings;
      const legacy = props.datasource.annotationMode === "legacy";
      return React.createElement("div", { "aria-label": "Standard SDK query editor" },
        React.createElement("p", null, "Editor datasource: " + settings.uid),
        React.createElement("p", null, "Editor range: " + props.range.from.valueOf() + " / " + props.range.to.valueOf()),
        React.createElement("p", null, "Editor streams: " + (props.datasource.activeStreams || 0) + " / " + (props.data?.state || "None")),
        React.createElement("label", null, legacy ? "Legacy annotation expression" : "SDK annotation text",
          React.createElement("input", { value: legacy ? props.annotation.query || "" : props.query.annotationText || "", onChange: event => legacy
            ? props.onAnnotationChange({ ...props.annotation, query: event.target.value })
            : props.onChange({ ...props.query, annotation: true, annotationText: event.target.value }) })),
        React.createElement("button", { type: "button", onClick: props.onRunQuery }, "Run from SDK editor"));
    }
    function CustomAnnotationEditor(props) {
      const settings = data.usePluginContext().instanceSettings;
      return React.createElement("div", { "aria-label": "Custom SDK annotation editor" },
        React.createElement("p", null, "Custom editor datasource: " + settings.uid),
        React.createElement("p", null, "Editor frames: " + (props.data?.series.length || 0)),
        React.createElement("label", null, "Custom annotation text", React.createElement("input", { value: props.query.annotationText || "", onChange: event => props.onAnnotationChange({ ...props.annotation, customEditorFlag: "preserved", target: { ...props.query, annotation: true, annotationText: event.target.value } }) })),
        React.createElement("button", { type: "button", onClick: props.onRunQuery }, "Run from custom SDK editor"));
    }
    class AnnotationFixture extends runtime.DataSourceWithBackend {
      constructor(settings) {
        super(settings);
        const mode = settings.jsonData.annotationMode;
        this.annotationMode = mode;
        if (mode === "legacy") {
          this.annotationQuery = async options => {
            const response = await rx.firstValueFrom(this.query({ requestId: "legacyAnnotations", range: options.range, rangeRaw: options.rangeRaw, interval: "1s", intervalMs: 1000, maxDataPoints: 1000, scopedVars: options.scopedVars, targets: [{ ...options.annotation.target, refId: "Anno", annotation: true, annotationText: "Legacy " + options.dashboard.uid + " " + (options.annotation.query || "$service") }] }));
            return events(response.data);
          };
        } else {
          this.annotations = {
            getDefaultQuery: () => ({ target: { annotation: true } }),
            prepareAnnotation: original => ({ ...original, target: { annotation: true, ...original.target } }),
            prepareQuery: annotation => annotation.target.skip ? undefined : annotation.target,
          };
          if (mode === "custom") {
            this.annotations.processEvents = (_annotation, frames) => rx.of(events(frames).map(event => ({ ...event, text: "Custom " + event.text, title: "Custom annotation" })));
            this.annotations.QueryEditor = CustomAnnotationEditor;
          }
        }
      }
      query(request) {
        const backend = super.query({ ...request, targets: request.targets.map(target => ({ ...target, annotationText: runtime.getTemplateSrv().replace(target.annotationText || target.query || "SDK $service / $__range_ms", request.scopedVars) })) });
        if (!request.targets.some(target => target.annotationFrontendStream)) return backend;
        return new rx.Observable(subscriber => {
          this.activeStreams = (this.activeStreams || 0) + 1;
          const subscription = backend.subscribe({ next: response => subscriber.next({ ...response, state: data.LoadingState.Streaming }), error: error => subscriber.error(error) });
          return () => { subscription.unsubscribe(); this.activeStreams--; };
        });
      }
    }
    _export("plugin", new data.DataSourcePlugin(AnnotationFixture).setQueryEditor(QueryEditor));
  } };
});
