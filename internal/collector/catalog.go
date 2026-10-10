package collector

// Integration describes an implemented collection path, not a running service.
type Integration struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Protocol string `json:"protocol"`
	Metrics  string `json:"metrics"`
	Docs     string `json:"docs"`
}

func Catalog() []Integration {
	return []Integration{
		{"prometheus", "Prometheus / OpenMetrics", "http://localhost:9100/metrics", "HTTP exposition", "Gauges, counters, classic histograms and summaries; any compatible exporter", "https://prometheus.io/docs/instrumenting/exporters/"},
		{"mysql", "MySQL / MariaDB", "mysql://localhost:3306", "SQL", "SHOW GLOBAL STATUS: connections, queries, InnoDB, replication counters", "https://dev.mysql.com/doc/refman/8.4/en/show-status.html"},
		{"redis", "Redis", "redis://localhost:6379", "RESP / TLS", "INFO ALL: memory, clients, commands, persistence, replication and keyspaces", "https://redis.io/docs/latest/commands/info/"},
		{"postgresql", "PostgreSQL", "postgresql://localhost:5432", "SQL", "pg_stat_database: connections, transactions, blocks, tuples, conflicts and deadlocks", "https://www.postgresql.org/docs/current/monitoring-stats.html"},
		{"clickhouse", "ClickHouse", "http://localhost:8123", "HTTP SQL", "system.metrics, system.asynchronous_metrics and system.events", "https://clickhouse.com/docs/operations/system-tables/metrics"},
		{"elasticsearch", "Elasticsearch", "http://localhost:9200/_nodes/stats", "HTTP JSON", "Node statistics: JVM, indices, filesystem, process, thread pools and breakers", "https://www.elastic.co/docs/api/doc/elasticsearch/operation/operation-nodes-stats"},
		{"hadoop", "Hadoop / YARN", "http://localhost:8088/jmx", "JMX HTTP JSON", "Numeric JMX attributes with MBean labels", "https://hadoop.apache.org/docs/stable/hadoop-project-dist/hadoop-common/Metrics.html"},
		{"hdfs", "HDFS", "http://localhost:9870/jmx", "JMX HTTP JSON", "NameNode / DataNode JMX: capacity, blocks, RPC and JVM", "https://hadoop.apache.org/docs/stable/hadoop-project-dist/hadoop-common/Metrics.html"},
		{"hive", "Hive", "http://localhost:8778/jolokia/read/metrics:*", "Jolokia read", "Numeric Hive metrics MBeans; requires a configured Jolokia agent", "https://hive.apache.org/docs/latest/admin/adminmanual-configuration/"},
		{"kafka", "Kafka", "http://localhost:8778/jolokia/read/kafka.*:*", "Jolokia read", "Broker, controller and topic JMX attributes; requires a configured Jolokia agent", "https://kafka.apache.org/documentation/#monitoring"},
		{"spark", "Spark", "http://localhost:4040/metrics/json", "HTTP JSON", "Configured MetricsServlet: gauges, counters, meters, histograms and timers", "https://spark.apache.org/docs/latest/monitoring.html"},
		{"flink", "Flink", "http://localhost:8081/jobmanager/metrics", "REST metrics", "JobManager metrics; also accepts TaskManager / job metrics URLs", "https://nightlies.apache.org/flink/flink-docs-stable/docs/ops/rest_api/"},
	}
}
