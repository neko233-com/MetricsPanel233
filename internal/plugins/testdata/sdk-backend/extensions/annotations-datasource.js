System.register(["@grafana/data", "@grafana/runtime", "rxjs"], function (_export) {
  let data, runtime, rx;
  return { setters: [m => data = m, m => runtime = m, m => rx = m], execute() {
    const events = frames => frames.flatMap(frame => Array.from({ length: frame.length }, (_, row) => {
      const get = name => frame.fields.find(field => field.name === name)?.values[row];
      return { id: get("EventKey"), time: get("When"), timeEnd: get("End"), text: get("Detail"), tags: get("Tags")?.split(",") || [] };
    }));
    class AnnotationFixture extends runtime.DataSourceWithBackend {
      constructor(settings) {
        super(settings);
        const mode = settings.jsonData.annotationMode;
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
          if (mode === "custom") this.annotations.processEvents = (_annotation, frames) => rx.of(events(frames).map(event => ({ ...event, text: "Custom " + event.text, title: "Custom annotation" })));
        }
      }
      query(request) {
        return super.query({ ...request, targets: request.targets.map(target => ({ ...target, annotationText: runtime.getTemplateSrv().replace(target.annotationText || target.query || "SDK $service / $__range_ms", request.scopedVars) })) });
      }
    }
    _export("plugin", new data.DataSourcePlugin(AnnotationFixture));
  } };
});
