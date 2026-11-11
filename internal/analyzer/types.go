package analyzer

// Verdict is intentionally tri-state: lack of evidence must not be reported
// as a healthy or failed condition.
type Verdict string

const (
	Healthy Verdict = "HEALTHY"
	Failed  Verdict = "FAILED"
	Unknown Verdict = "UNKNOWN"
)

type Evidence struct {
	Check      string   `json:"check" yaml:"check"`
	Verdict    Verdict  `json:"verdict" yaml:"verdict"`
	Summary    string   `json:"summary" yaml:"summary"`
	Details    []string `json:"details,omitempty" yaml:"details,omitempty"`
	Suggestion string   `json:"suggestion,omitempty" yaml:"suggestion,omitempty"`
}

type ContainerInfo struct {
	Name         string            `json:"name" yaml:"name"`
	Image        string            `json:"image" yaml:"image"`
	Ready        bool              `json:"ready" yaml:"ready"`
	RestartCount int32             `json:"restartCount" yaml:"restartCount"`
	State        string            `json:"state" yaml:"state"`
	Reason       string            `json:"reason,omitempty" yaml:"reason,omitempty"`
	Requests     map[string]string `json:"requests,omitempty" yaml:"requests,omitempty"`
	Limits       map[string]string `json:"limits,omitempty" yaml:"limits,omitempty"`
}

type EventInfo struct {
	Type      string `json:"type" yaml:"type"`
	Reason    string `json:"reason" yaml:"reason"`
	Message   string `json:"message" yaml:"message"`
	Count     int32  `json:"count,omitempty" yaml:"count,omitempty"`
	Timestamp string `json:"timestamp,omitempty" yaml:"timestamp,omitempty"`
	Source    string `json:"source,omitempty" yaml:"source,omitempty"`
}

type ContainerLogs struct {
	Name         string `json:"name" yaml:"name"`
	Current      string `json:"current,omitempty" yaml:"current,omitempty"`
	Previous     string `json:"previous,omitempty" yaml:"previous,omitempty"`
	CurrentError string `json:"currentError,omitempty" yaml:"currentError,omitempty"`
	PreviousError string `json:"previousError,omitempty" yaml:"previousError,omitempty"`
}

type PodReport struct {
	Namespace      string          `json:"namespace" yaml:"namespace"`
	Name           string          `json:"name" yaml:"name"`
	Phase          string          `json:"phase" yaml:"phase"`
	Ready          bool            `json:"ready" yaml:"ready"`
	Restarts       int32           `json:"restarts" yaml:"restarts"`
	Node           string          `json:"node,omitempty" yaml:"node,omitempty"`
	PodIP          string          `json:"podIP,omitempty" yaml:"podIP,omitempty"`
	QOSClass       string          `json:"qosClass,omitempty" yaml:"qosClass,omitempty"`
	Age            string          `json:"age" yaml:"age"`
	ServiceAccount string          `json:"serviceAccount,omitempty" yaml:"serviceAccount,omitempty"`
	OwnerChain     []string        `json:"ownerChain,omitempty" yaml:"ownerChain,omitempty"`
	Labels         map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Containers     []ContainerInfo `json:"containers" yaml:"containers"`
	InboundServices []ServiceTrace `json:"inboundServices,omitempty" yaml:"inboundServices,omitempty"`
	IngressRoutes   []IngressTrace `json:"ingressRoutes,omitempty" yaml:"ingressRoutes,omitempty"`
	Events          []EventInfo    `json:"events,omitempty" yaml:"events,omitempty"`
	Logs            []ContainerLogs `json:"logs,omitempty" yaml:"logs,omitempty"`
	Evidence       []Evidence      `json:"evidence" yaml:"evidence"`
	Verdict        Verdict         `json:"verdict" yaml:"verdict"`
}
