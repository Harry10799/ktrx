package analyzer

import (
	"context"
	"strings"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

const containerLogTailLines int64 = 100

func collectContainerLogs(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod, includeAll bool) ([]ContainerLogs, []Evidence) {
	var results []ContainerLogs
	var evidence []Evidence
	for _, container := range pod.Spec.Containers {
		entry := ContainerLogs{Name: container.Name}
		tail := containerLogTailLines
		current, err := client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: container.Name, TailLines: &tail,
		}).Do(ctx).Raw()
		if err != nil {
			entry.CurrentError = err.Error()
			evidence = append(evidence, Evidence{
				Check: "container-logs/" + container.Name, Verdict: Unknown,
				Summary: "Could not retrieve current container logs",
				Details: []string{err.Error()},
				Suggestion: "Check pod log access permissions and whether the container has started.",
			})
		} else {
			entry.Current = filterLogLines(strings.TrimSpace(string(current)), includeAll)
		}

		var status *corev1.ContainerStatus
		for i := range pod.Status.ContainerStatuses {
			if pod.Status.ContainerStatuses[i].Name == container.Name {
				status = &pod.Status.ContainerStatuses[i]
				break
			}
		}
		if status != nil && status.RestartCount > 0 {
			previous, previousErr := client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: container.Name, Previous: true, TailLines: &tail,
			}).Do(ctx).Raw()
			if previousErr != nil {
				entry.PreviousError = previousErr.Error()
				evidence = append(evidence, Evidence{
					Check: "previous-container-logs/" + container.Name, Verdict: Unknown,
					Summary: "Could not retrieve previous container logs",
					Details: []string{previousErr.Error()},
					Suggestion: "Check whether a previous container instance exists and whether pod log access is allowed.",
				})
			} else {
				entry.Previous = filterLogLines(strings.TrimSpace(string(previous)), includeAll)
			}
		}
		results = append(results, entry)
	}
	return results, evidence
}

var noisyLogPattern = regexp.MustCompile(`(?i)(\b(error|fatal|panic|exception|fail(ed|ure)?|critical)\b|\b[45][0-9]{2}\b|status[=: ]+[45][0-9]{2})`)

func filterLogLines(raw string, includeAll bool) string {
	if includeAll || strings.TrimSpace(raw) == "" { return raw }
	lines := strings.Split(raw, "\n")
	var selected []string
	for _, line := range lines {
		if noisyLogPattern.MatchString(line) { selected = append(selected, line) }
	}
	if len(selected) == 0 { return "" }
	return strings.Join(selected, "\n")
}
