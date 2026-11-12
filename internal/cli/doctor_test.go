package cli

import "testing"

func TestDoctorIncludesTimelineAndOptionalGitOpsPermissions(t *testing.T) {
	requirements := ktrxAccessRequirements("production", false)
	find := func(group, resource, verb string) (accessRequirement, bool) {
		for _, requirement := range requirements {
			if requirement.group == group && requirement.resource == resource && requirement.verb == verb {
				return requirement, true
			}
		}
		return accessRequirement{}, false
	}
	for _, item := range []struct{ group, resource, verb string }{
		{"apps", "deployments", "get"},
		{"apps", "replicasets", "list"},
		{"", "configmaps", "get"},
		{"", "secrets", "get"},
	} {
		if _, ok := find(item.group, item.resource, item.verb); !ok {
			t.Errorf("missing required timeline permission %s %s %s", item.group, item.resource, item.verb)
		}
	}
	for _, item := range []struct{ group, resource string }{
		{"gateway.networking.k8s.io", "httproutes"},
		{"elbv2.k8s.aws", "targetgroupbindings"},
	} {
		requirement, ok := find(item.group, item.resource, "list")
		if !ok || !requirement.optional {
			t.Errorf("expected %s %s permission to be optional", item.group, item.resource)
		}
	}
	for _, item := range []struct{ group, resource string }{
		{"kustomize.toolkit.fluxcd.io", "kustomizations"},
		{"helm.toolkit.fluxcd.io", "helmreleases"},
		{"source.toolkit.fluxcd.io", "gitrepositories"},
		{"argoproj.io", "applications"},
	} {
		requirement, ok := find(item.group, item.resource, "list")
		if item.resource == "gitrepositories" {
			requirement, ok = find(item.group, item.resource, "get")
		}
		if !ok || !requirement.optional {
			t.Errorf("expected %s %s permission to be optional", item.group, item.resource)
		}
	}
}
