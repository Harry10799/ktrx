package cli

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
)

type elbv2TargetHealthAPI interface {
	DescribeTargetHealth(context.Context, *elbv2.DescribeTargetHealthInput, ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error)
}

type urlTraceAWSTarget struct {
	ID             string `json:"id"`
	Port           int32  `json:"port,omitempty"`
	AvailabilityZone string `json:"availabilityZone,omitempty"`
	State          string `json:"state"`
	Reason         string `json:"reason,omitempty"`
	Description    string `json:"description,omitempty"`
	KubernetesPod  string `json:"kubernetesPod,omitempty"`
}

type urlTraceAWSTargetGroupHealth struct {
	TargetGroupARN string             `json:"targetGroupARN"`
	Status         string             `json:"status"`
	Targets        []urlTraceAWSTarget `json:"targets,omitempty"`
	Error          string             `json:"error,omitempty"`
}

func awsRegionFromTargetGroupARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 || parts[0] != "arn" || parts[2] != "elasticloadbalancing" {
		return ""
	}
	return parts[3]
}

func describeAWSTargetGroupHealth(ctx context.Context, client elbv2TargetHealthAPI, arn string) (*urlTraceAWSTargetGroupHealth, error) {
	if strings.TrimSpace(arn) == "" {
		return nil, fmt.Errorf("target group ARN is empty")
	}
	out, err := client.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(arn)})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("AWS returned an empty DescribeTargetHealth response")
	}
	result := &urlTraceAWSTargetGroupHealth{TargetGroupARN: arn, Status: "AVAILABLE"}
	for _, item := range out.TargetHealthDescriptions {
		if item == nil || item.Target == nil {
			continue
		}
		target := urlTraceAWSTarget{
			ID:               aws.ToString(item.Target.Id),
			Port:             aws.ToInt32(item.Target.Port),
			AvailabilityZone: aws.ToString(item.Target.AvailabilityZone),
		}
		if item.TargetHealth != nil {
			target.State = string(item.TargetHealth.State)
			target.Reason = string(item.TargetHealth.Reason)
			target.Description = aws.ToString(item.TargetHealth.Description)
		}
		result.Targets = append(result.Targets, target)
	}
	if len(result.Targets) == 0 {
		result.Status = "NO_TARGETS"
	}
	return result, nil
}

func attachAWSTargetHealth(ctx context.Context, backend *urlTraceBackend, regionOverride string, cache map[string]*urlTraceAWSTargetGroupHealth, limitations *[]string) {
	for i := range backend.TargetGroupBindings {
		binding := &backend.TargetGroupBindings[i]
		arn := binding.TargetGroupARN
		if arn == "" {
			continue
		}
		if cached, ok := cache[arn]; ok {
			backend.AWSTargetGroups = append(backend.AWSTargetGroups, *cached)
			continue
		}
		region := regionOverride
		if region == "" {
			region = awsRegionFromTargetGroupARN(arn)
		}
		if region == "" {
			health := &urlTraceAWSTargetGroupHealth{TargetGroupARN: arn, Status: "UNAVAILABLE", Error: "could not determine AWS region from TargetGroupBinding ARN; pass --aws-region"}
			cache[arn] = health
			backend.AWSTargetGroups = append(backend.AWSTargetGroups, *health)
			*limitations = appendURLTraceNote(*limitations, "AWS target health unavailable for "+binding.Name+": "+health.Error)
			continue
		}
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			health := &urlTraceAWSTargetGroupHealth{TargetGroupARN: arn, Status: "UNAVAILABLE", Error: err.Error()}
			cache[arn] = health
			backend.AWSTargetGroups = append(backend.AWSTargetGroups, *health)
			*limitations = appendURLTraceNote(*limitations, "AWS target health unavailable for "+binding.Name+": "+err.Error())
			continue
		}
		health, err := describeAWSTargetGroupHealth(ctx, elbv2.NewFromConfig(cfg), arn)
		if err != nil {
			health = &urlTraceAWSTargetGroupHealth{TargetGroupARN: arn, Status: "UNAVAILABLE", Error: err.Error()}
			*limitations = appendURLTraceNote(*limitations, "AWS target health unavailable for "+binding.Name+": "+err.Error())
		}
		correlateAWSTargetsWithPods(health.Targets, backend.Pods)
		cache[arn] = health
		backend.AWSTargetGroups = append(backend.AWSTargetGroups, *health)
	}
}

func correlateAWSTargetsWithPods(targets []urlTraceAWSTarget, pods []urlTracePod) {
	for i := range targets {
		targetIP := net.ParseIP(targets[i].ID)
		if targetIP == nil {
			continue
		}
		for _, pod := range pods {
			for _, podIP := range pod.IPs {
				if parsed := net.ParseIP(podIP); parsed != nil && parsed.Equal(targetIP) {
					targets[i].KubernetesPod = pod.Name
					break
				}
			}
			if targets[i].KubernetesPod != "" {
				break
			}
		}
	}
}

func applyAWSTargetHealthVerdict(report *urlTraceReport) {
	hasHealthy, hasUnhealthy := false, false
	if len(report.Routes) == 0 {
		return
	}
	// The URL trace verdict is based on the selected route. Do not let an
	// unrelated, lower-precedence route change the result.
	for _, backend := range report.Routes[0].Backends {
		for _, group := range backend.AWSTargetGroups {
			for _, target := range group.Targets {
				switch strings.ToLower(target.State) {
				case "healthy":
					hasHealthy = true
				case "unhealthy":
					hasUnhealthy = true
				}
			}
		}
	}
	if !hasUnhealthy {
		return
	}
	if hasHealthy {
		if report.Verdict != "FAILED" {
			report.Verdict = "WARN"
		}
		return
	}
	report.Verdict = "FAILED"
}
