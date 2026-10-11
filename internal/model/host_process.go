package model

// HostProcess describes the processes visible to the server, never its HTTP client.
type HostProcess struct {
	ID              string            `json:"id"`
	PID             int               `json:"pid"`
	PPID            int               `json:"ppid"`
	Name            string            `json:"name"`
	NameSource      string            `json:"nameSource"`
	StartToken      string            `json:"startToken"`
	StartedAt       *int64            `json:"startedAt"`
	CPUSecondsTotal *float64          `json:"cpuSecondsTotal"`
	CPUPercent      *float64          `json:"cpuPercent"`
	MemoryRSSBytes  *string           `json:"memoryRssBytes"`
	Threads         *int              `json:"threads"`
	IconKey         string            `json:"iconKey"`
	States          map[string]string `json:"states"`
}

type HostProcessCapabilities struct {
	CPU    bool `json:"cpu"`
	Memory bool `json:"memory"`
	Icons  bool `json:"icons"`
}
type HostProcessSource struct {
	ID           string                  `json:"id"`
	Hostname     string                  `json:"hostname"`
	OS           string                  `json:"os"`
	Scope        string                  `json:"scope"`
	Capabilities HostProcessCapabilities `json:"capabilities"`
}
type HostProcessList struct {
	Source        HostProcessSource `json:"source"`
	SnapshotID    string            `json:"snapshotId"`
	SampledAt     int64             `json:"sampledAt"`
	ExpiresAt     int64             `json:"expiresAt"`
	Prefix        string            `json:"prefix"`
	Order         string            `json:"order"`
	Total         int               `json:"total"`
	Items         []HostProcess     `json:"items"`
	ContinueToken string            `json:"continueToken"`
	Partial       bool              `json:"partial"`
	Warnings      []string          `json:"warnings"`
}
type HostProcessDetail struct {
	Source    HostProcessSource `json:"source"`
	SampledAt int64             `json:"sampledAt"`
	Process   HostProcess       `json:"process"`
}
