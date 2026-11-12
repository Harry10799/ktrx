package timeline

import "time"

type EventType string

const (
	EventRollout EventType = "ROLLOUT"
	EventConfig  EventType = "CONFIG"
)

type Change struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type TimelineEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Type       EventType `json:"type"`
	Resource   string    `json:"resource"`
	Revision   string    `json:"revision,omitempty"`
	Action     string    `json:"action"`
	Changes    []Change  `json:"changes,omitempty"`
	Source     string    `json:"source"`
	Confidence string    `json:"confidence"`
	Details    string    `json:"details,omitempty"`
}

type Report struct {
	Namespace   string          `json:"namespace"`
	Target      string          `json:"target"`
	GeneratedAt time.Time       `json:"generatedAt"`
	Events      []TimelineEvent `json:"events"`
	Limitations []string        `json:"limitations,omitempty"`
}
