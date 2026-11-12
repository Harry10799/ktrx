package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/Harry10799/ktrx/internal/kube"
	"github.com/spf13/cobra"
)

type netcheckResult struct {
	Type     string `json:"type"`
	Target   string `json:"target"`
	Status   string `json:"status"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
	Duration string `json:"duration"`
}

type netcheckReport struct {
	Namespace string           `json:"namespace"`
	Pod       string           `json:"pod"`
	Container string           `json:"container"`
	Results   []netcheckResult `json:"results"`
}

func newNetcheckCommand() *cobra.Command {
	var namespace, container, kubeconfig, contextName, output string
	var dnsTarget, tcpTarget, httpTarget string
	var timeout time.Duration
	var noColor bool
	cmd := &cobra.Command{
		Use:   "netcheck POD",
		Short: "Run explicit DNS, TCP and HTTP checks from a selected Pod",
		Long:  "Executes only the probes requested by the user inside one existing Pod container. No resources are created or changed. The target container must provide /bin/sh and the relevant diagnostic utility.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dnsTarget == "" && tcpTarget == "" && httpTarget == "" {
				return fmt.Errorf("specify at least one of --dns, --tcp or --http")
			}
			if timeout <= 0 || timeout > 2*time.Minute {
				return fmt.Errorf("--timeout must be greater than 0 and no more than 2m")
			}
			if dnsTarget != "" && strings.TrimSpace(dnsTarget) != dnsTarget {
				return fmt.Errorf("--dns must not contain leading or trailing spaces")
			}
			var tcpHost, tcpPort string
			if tcpTarget != "" {
				var err error
				tcpHost, tcpPort, err = net.SplitHostPort(tcpTarget)
				if err != nil || tcpHost == "" {
					return fmt.Errorf("--tcp must be host:port (IPv6 must use [address]:port)")
				}
				port, err := strconv.Atoi(tcpPort)
				if err != nil || port < 1 || port > 65535 {
					return fmt.Errorf("--tcp port must be between 1 and 65535")
				}
			}
			if httpTarget != "" {
				u, err := url.ParseRequestURI(httpTarget)
				if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
					return fmt.Errorf("--http must be a valid http:// or https:// URL")
				}
			}

			config, err := kube.NewRESTConfig(kubeconfig, contextName, timeout)
			if err != nil {
				return err
			}
			client, err := kubernetes.NewForConfig(config)
			if err != nil {
				return fmt.Errorf("create Kubernetes client: %w", err)
			}
			pod, err := client.CoreV1().Pods(namespace).Get(cmd.Context(), args[0], metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("get Pod %s/%s: %w", namespace, args[0], err)
			}
			if pod.Status.Phase != corev1.PodRunning {
				return fmt.Errorf("Pod %s/%s is %s; netcheck requires a Running Pod", namespace, args[0], pod.Status.Phase)
			}
			if container == "" {
				if len(pod.Spec.Containers) != 1 {
					return fmt.Errorf("Pod has %d containers; specify --container to choose the probe container", len(pod.Spec.Containers))
				}
				container = pod.Spec.Containers[0].Name
			} else {
				found := false
				for _, c := range pod.Spec.Containers {
					if c.Name == container {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("container %q is not a regular container in Pod %s/%s", container, namespace, args[0])
				}
			}

			containerRunning := false
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == container && status.State.Running != nil {
					containerRunning = true
					break
				}
			}
			if !containerRunning {
				return fmt.Errorf("container %q in Pod %s/%s is not running", container, namespace, args[0])
			}

			report := netcheckReport{Namespace: namespace, Pod: args[0], Container: container}
			probeTimeout := int(timeout.Seconds())
			if probeTimeout < 1 {
				probeTimeout = 1
			}
			run := func(kind, target, script string) {
				start := time.Now()
				ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
				defer cancel()
				stdout, stderr, execErr := execInPod(ctx, config, client, namespace, args[0], container, script)
				result := netcheckResult{Type: kind, Target: target, Output: strings.TrimSpace(stdout), Duration: time.Since(start).Round(time.Millisecond).String()}
				if execErr == nil {
					result.Status = "PASS"
				} else if strings.Contains(stdout+stderr, "KTRX_TOOL_UNAVAILABLE") {
					result.Status = "UNAVAILABLE"
					result.Error = "required utility is not installed in the selected container"
				} else {
					result.Status = "FAIL"
					result.Error = strings.TrimSpace(execErr.Error())
					if strings.TrimSpace(stderr) != "" {
						result.Error += ": " + strings.TrimSpace(stderr)
					}
				}
				report.Results = append(report.Results, result)
			}
			if dnsTarget != "" {
				q := shellQuote(dnsTarget)
				nslookupName := shellQuote(dnsLookupName(dnsTarget, namespace))
				run("DNS", dnsTarget, "if command -v getent >/dev/null 2>&1; then getent hosts "+q+"; elif command -v nslookup >/dev/null 2>&1; then nslookup "+nslookupName+"; else echo KTRX_TOOL_UNAVAILABLE; exit 127; fi")
			}
			if tcpTarget != "" {
				run("TCP", tcpTarget, "if command -v nc >/dev/null 2>&1; then nc -z -w "+strconv.Itoa(probeTimeout)+" "+shellQuote(tcpHost)+" "+shellQuote(tcpPort)+"; else echo KTRX_TOOL_UNAVAILABLE; exit 127; fi")
			}
			if httpTarget != "" {
				q := shellQuote(httpTarget)
				run("HTTP", httpTarget, "if command -v curl >/dev/null 2>&1; then curl -fSs -o /dev/null -w 'HTTP_STATUS=%{http_code}\\n' --max-time "+strconv.Itoa(probeTimeout)+" "+q+"; elif command -v wget >/dev/null 2>&1; then wget --spider -T "+strconv.Itoa(probeTimeout)+" -S "+q+"; else echo KTRX_TOOL_UNAVAILABLE; exit 127; fi")
			}
			if strings.EqualFold(output, "json") {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
			} else if strings.EqualFold(output, "terminal") || strings.EqualFold(output, "text") {
				printNetcheck(cmd.OutOrStdout(), report, !noColor && os.Getenv("NO_COLOR") == "")
			} else {
				return fmt.Errorf("unsupported output %q (choose terminal or json)", output)
			}
			hasUnavailable := false
			for _, result := range report.Results {
				switch result.Status {
				case "FAIL":
					return exitCodeErrorf(2, "one or more network checks failed")
				case "UNAVAILABLE":
					hasUnavailable = true
				}
			}
			if hasUnavailable { return exitCodeErrorf(3, "one or more network checks could not run") }
			return nil
		},
	}
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Pod namespace")
	cmd.Flags().StringVar(&container, "container", "", "Container to run probes in (required for multi-container Pods)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&contextName, "context", "", "Kubeconfig context")
	cmd.Flags().StringVar(&output, "output", "terminal", "Output format: terminal or json")
	cmd.Flags().StringVar(&dnsTarget, "dns", "", "DNS hostname to resolve")
	cmd.Flags().StringVar(&tcpTarget, "tcp", "", "TCP destination in host:port format")
	cmd.Flags().StringVar(&httpTarget, "http", "", "HTTP(S) URL to request")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Timeout per probe (maximum 2m)")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "Disable colored terminal output")
	return cmd
}

func dnsLookupName(target, namespace string) string {
	// Avoid resolver search-list expansion in nslookup implementations such as
	// BusyBox, which can return a non-zero status after a valid short-name
	// answer because later search suffixes return NXDOMAIN.
	if !strings.Contains(target, ".") {
		return target + "." + namespace + ".svc.cluster.local."
	}
	return strings.TrimSuffix(target, ".") + "."
}

func execInPod(ctx context.Context, config *rest.Config, client kubernetes.Interface, namespace, pod, container, script string) (string, string, error) {
	req := client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   []string{"/bin/sh", "-c", script},
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("create Pod exec stream: %w", err)
	}
	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})
	return stdout.String(), stderr.String(), err
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func printNetcheck(w io.Writer, report netcheckReport, colorEnabled bool) {
	style := func(color, value string) string {
		if !colorEnabled {
			return value
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
	}
	fmt.Fprintf(w, "%s  %s/%s (%s)\n", style("39", "KTRX NETCHECK"), report.Namespace, report.Pod, report.Container)
	for _, result := range report.Results {
		color := "42"
		symbol := "✓"
		if result.Status == "FAIL" {
			color, symbol = "196", "✖"
		}
		if result.Status == "UNAVAILABLE" {
			color, symbol = "214", "!"
		}
		fmt.Fprintf(w, "  %s [%s] %s (%s)\n", style(color, symbol+" "+result.Status), result.Type, result.Target, result.Duration)
		if result.Output != "" {
			fmt.Fprintf(w, "      %s\n", strings.ReplaceAll(result.Output, "\n", "\n      "))
		}
		if result.Error != "" {
			fmt.Fprintf(w, "      %s\n", result.Error)
		}
	}
}
