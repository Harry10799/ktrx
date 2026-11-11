package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// NewClient builds a Kubernetes client using an explicit kubeconfig, when
// supplied, or the standard loading rules (KUBECONFIG / ~/.kube/config).
func NewClient(kubeconfig, context string) (kubernetes.Interface, error) {
	return NewClientWithTimeout(kubeconfig, context, 0)
}

// NewClientWithTimeout builds a Kubernetes client with an optional per-request timeout.
func NewClientWithTimeout(kubeconfig, context string, timeout time.Duration) (kubernetes.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	} else if env := os.Getenv("KUBECONFIG"); env == "" && homedir.HomeDir() != "" {
		loadingRules.Precedence = []string{filepath.Join(homedir.HomeDir(), ".kube", "config")}
	}

	overrides := &clientcmd.ConfigOverrides{}
	if context != "" {
		overrides.CurrentContext = context
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	config.Timeout = timeout
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return client, nil
}
