// Protocol fixtures validate metrics API contracts without starting large JVM
// clusters. Real SQL/RESP services are started separately in the same container.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	mux := http.NewServeMux()
	fixtures := map[string]string{
		"/hadoop":        `{"beans":[{"name":"Hadoop:service=ResourceManager,name=JvmMetrics","MemHeapUsedM":233}]}`,
		"/hdfs":          `{"beans":[{"name":"Hadoop:service=NameNode,name=FSNamesystem","CapacityUsed":233}]}`,
		"/hive":          `{"status":200,"value":{"metrics:name=connections":{"Count":233}}}`,
		"/kafka":         `{"status":200,"value":{"kafka.server:type=BrokerTopicMetrics,name=MessagesInPerSec":{"Count":233}}}`,
		"/spark":         `{"gauges":{"driver.JVM.heap.used":{"value":233}},"counters":{},"timers":{}}`,
		"/elasticsearch": `{"nodes":{"node-233":{"name":"data","jvm":{"mem":{"heap_used_in_bytes":233}}}}}`,
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "metrics" || password != "integration-fixture-password" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/prometheus" {
			w.Header().Set("Content-Type", "application/openmetrics-text; version=1.0.0")
			fmt.Fprint(w, "# TYPE fixture_requests counter\nfixture_requests_total 233 # {trace_id=\"fixture\"} 1\n# EOF\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/flink" {
			if r.URL.Query().Get("get") == "" {
				fmt.Fprint(w, `[{"id":"Status.JVM.Memory.Heap.Used"}]`)
			} else {
				fmt.Fprint(w, `[{"id":"Status.JVM.Memory.Heap.Used","value":"233"}]`)
			}
			return
		}
		if body, ok := fixtures[r.URL.Path]; ok {
			fmt.Fprint(w, body)
			return
		}
		w.WriteHeader(404)
	})
	if err := http.ListenAndServe("127.0.0.1:9099", mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
