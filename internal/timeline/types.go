package timeline

import "time"

type EventType string

const (
	EventRollout    EventType = "ROLLOUT"
	EventConfig     EventType = "CONFIG"
	EventKubernetes EventType = "KUBERNETES"
	EventFailure    EventType = "FAILURE"
	EventRestart    EventType = "RESTART"
	EventGitOps     EventType = "GITOPS"
)

type Change struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type TimelineEvent struct {
	Timestamp  *time.Time `json:"timestamp,omitempty"`
	SortTime   *time.Time `json:"-"`
	Type       EventType  `json:"type"`
	Resource   string     `json:"resource"`
	Revision   string     `json:"revision,omitempty"`
	Current    bool       `json:"current,omitempty"`
	Action     string     `json:"action"`
	Changes    []Change   `json:"changes,omitempty"`
	Source     string     `json:"source"`
	Confidence string     `json:"confidence"`
	Details    string     `json:"details,omitempty"`
}

type Correlation struct {
	Timestamp  *time.Time `json:"timestamp,omitempty"`
	Type       EventType  `json:"type"`
	Resource   string     `json:"resource"`
	Summary    string     `json:"summary"`
	Rank       int        `json:"rank"`
	Confidence string     `json:"confidence"`
	Disclaimer string     `json:"disclaimer"`
}

type Report struct {
	Namespace        string          `json:"namespace"`
	Target           string          `json:"target"`
	GeneratedAt      time.Time       `json:"generatedAt"`
	Since            time.Time       `json:"since"`
	FailureStartedAt *time.Time      `json:"failureStartedAt,omitempty"`
	Events           []TimelineEvent `json:"events"`
	Correlations     []Correlation   `json:"potentialCorrelations,omitempty"`
	Limitations      []string        `json:"limitations,omitempty"`
}
