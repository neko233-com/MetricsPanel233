package collector

type ExporterPreset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Docs     string `json:"docs"`
}

// Presets configure HTTP scraping of exporters running on monitored hosts.
func Exporters() []ExporterPreset {
	return []ExporterPreset{
		{"node", "Linux / Node exporter", "http://localhost:9100/metrics", "https://github.com/prometheus/node_exporter"},
		{"windows", "Windows exporter", "http://localhost:9182/metrics", "https://github.com/prometheus-community/windows_exporter"},
		{"process", "Process exporter", "http://localhost:9256/metrics", "https://github.com/ncabatoff/process-exporter"},
		{"mysql", "MySQL exporter", "http://localhost:9104/metrics", "https://github.com/prometheus/mysqld_exporter"},
		{"redis", "Redis exporter", "http://localhost:9121/metrics", "https://github.com/oliver006/redis_exporter"},
		{"postgresql", "PostgreSQL exporter", "http://localhost:9187/metrics", "https://github.com/prometheus-community/postgres_exporter"},
		{"elasticsearch", "Elasticsearch exporter", "http://localhost:9114/metrics", "https://github.com/prometheus-community/elasticsearch_exporter"},
		{"kafka", "Kafka exporter", "http://localhost:9308/metrics", "https://github.com/danielqsj/kafka_exporter"},
		{"jmx", "JMX / Kafka / Hive / Hadoop", "http://localhost:9404/metrics", "https://github.com/prometheus/jmx_exporter"},
		{"cadvisor", "Container / cAdvisor", "http://localhost:8080/metrics", "https://github.com/google/cadvisor"},
		{"kubernetes", "Kubernetes / kube-state-metrics", "http://localhost:8080/metrics", "https://github.com/kubernetes/kube-state-metrics"},
		{"mongodb", "MongoDB exporter", "http://localhost:9216/metrics", "https://github.com/percona/mongodb_exporter"},
		{"rabbitmq", "RabbitMQ Prometheus plugin", "http://localhost:15692/metrics", "https://www.rabbitmq.com/docs/prometheus"},
		{"nginx", "Nginx exporter", "http://localhost:9113/metrics", "https://github.com/nginx/nginx-prometheus-exporter"},
		{"apache", "Apache exporter", "http://localhost:9117/metrics", "https://github.com/Lusitaniae/apache_exporter"},
		{"memcached", "Memcached exporter", "http://localhost:9150/metrics", "https://github.com/prometheus/memcached_exporter"},
		{"blackbox", "HTTP / TCP / ICMP probe", "http://localhost:9115/probe?module=http_2xx&target=http%3A%2F%2Flocalhost%3A7333", "https://github.com/prometheus/blackbox_exporter"},
		{"snmp", "SNMP device", "http://localhost:9116/snmp?module=if_mib&target=127.0.0.1", "https://github.com/prometheus/snmp_exporter"},
	}
}
