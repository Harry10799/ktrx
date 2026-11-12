package timeline

import (
 "fmt"
 "reflect"
 "sort"
 "strings"

 appsv1 "k8s.io/api/apps/v1"
 corev1 "k8s.io/api/core/v1"
)

func compareTemplates(before, after *appsv1.ReplicaSet) []Change {
 var changes []Change
 compareContainers(&changes,before.Spec.Template.Spec.Containers,after.Spec.Template.Spec.Containers,"containers")
 compareContainers(&changes,before.Spec.Template.Spec.InitContainers,after.Spec.Template.Spec.InitContainers,"initContainers")
 a,b:=before.Spec.Template.Spec,after.Spec.Template.Spec
 addStringMapChanges(&changes,"pod.annotations",before.Spec.Template.Annotations,after.Spec.Template.Annotations)
 addChange(&changes,"pod.serviceAccountName",a.ServiceAccountName,b.ServiceAccountName)
 addChange(&changes,"pod.nodeSelector",formatMap(a.NodeSelector),formatMap(b.NodeSelector))
 if !reflect.DeepEqual(a.Tolerations,b.Tolerations) { addChange(&changes,"pod.tolerations",formatTolerations(a.Tolerations),formatTolerations(b.Tolerations)) }
 if !reflect.DeepEqual(a.Volumes,b.Volumes) { addChange(&changes,"pod.volumes",formatVolumes(a.Volumes),formatVolumes(b.Volumes)) }
 return changes
}
func compareContainers(changes *[]Change,before,after []corev1.Container,prefix string) {
 left,right:=containerIndex(before),containerIndex(after); keys:=map[string]struct{}{}
 for k:=range left {keys[k]=struct{}{}}; for k:=range right {keys[k]=struct{}{}}
 for _,name:=range sortedKeys(keys) {
  old,ok1:=left[name]; cur,ok2:=right[name]; field:=prefix+"."+name
  if !ok1 {*changes=append(*changes,Change{Field:field,Before:"<absent>",After:"present"});continue}
  if !ok2 {*changes=append(*changes,Change{Field:field,Before:"present",After:"<absent>"});continue}
  addChange(changes,field+".image",old.Image,cur.Image)
  compareResources(changes,field,"requests",old.Resources.Requests,cur.Resources.Requests)
  compareResources(changes,field,"limits",old.Resources.Limits,cur.Resources.Limits)
  compareEnv(changes,field,old.Env,cur.Env); compareEnvFrom(changes,field,old.EnvFrom,cur.EnvFrom)
  if !reflect.DeepEqual(old.Command,cur.Command) {addChange(changes,field+".command","<redacted>","<redacted>")}
  if !reflect.DeepEqual(old.Args,cur.Args) {addChange(changes,field+".args","<redacted>","<redacted>")}
  if !reflect.DeepEqual(old.Ports,cur.Ports) {addChange(changes,field+".ports",formatPorts(old.Ports),formatPorts(cur.Ports))}
  compareProbe(changes,field+".readinessProbe",old.ReadinessProbe,cur.ReadinessProbe)
  compareProbe(changes,field+".livenessProbe",old.LivenessProbe,cur.LivenessProbe)
  compareProbe(changes,field+".startupProbe",old.StartupProbe,cur.StartupProbe)
 }
}
func compareProbe(c *[]Change,field string,a,b *corev1.Probe) {
 if reflect.DeepEqual(a,b){return}; addChange(c,field,probeSummary(a),probeSummary(b))
}
func probeSummary(p *corev1.Probe) string {
 if p==nil{return "<disabled>"}; action:="unknown"
 switch {case p.HTTPGet!=nil: action=fmt.Sprintf("httpGet path=%s port=%s scheme=%s",p.HTTPGet.Path,p.HTTPGet.Port.String(),p.HTTPGet.Scheme)
 case p.TCPSocket!=nil: action=fmt.Sprintf("tcpSocket port=%s",p.TCPSocket.Port.String())
 case p.Exec!=nil: action="exec command=<redacted>"
 case p.GRPC!=nil: action=fmt.Sprintf("grpc port=%d",p.GRPC.Port)}
 return fmt.Sprintf("%s initialDelay=%d timeout=%d period=%d success=%d failure=%d",action,p.InitialDelaySeconds,p.TimeoutSeconds,p.PeriodSeconds,p.SuccessThreshold,p.FailureThreshold)
}
func formatPorts(items []corev1.ContainerPort) string {v:=[]string{};for _,p:=range items{v=append(v,fmt.Sprintf("%s:%d/%s",p.Name,p.ContainerPort,p.Protocol))};sort.Strings(v);return strings.Join(v,",")}
func formatVolumes(items []corev1.Volume) string {
 v:=[]string{};for _,x:=range items{s:=x.Name+"=";switch{case x.ConfigMap!=nil:s+="configMap/"+x.ConfigMap.Name;case x.Secret!=nil:s+="secret/"+x.Secret.SecretName;case x.PersistentVolumeClaim!=nil:s+="pvc/"+x.PersistentVolumeClaim.ClaimName;case x.Projected!=nil:
  parts:=[]string{};for _,p:=range x.Projected.Sources{if p.ConfigMap!=nil{parts=append(parts,"configMap/"+p.ConfigMap.Name)};if p.Secret!=nil{parts=append(parts,"secret/"+p.Secret.Name)}};sort.Strings(parts);s+=strings.Join(parts,"+")
 default:s+="other"};v=append(v,s)};sort.Strings(v);return strings.Join(v,",")
}
func formatMap(m map[string]string) string {k:=[]string{};for x:=range m{k=append(k,x)};sort.Strings(k);v:=[]string{};for _,x:=range k{v=append(v,x+"="+m[x])};return strings.Join(v,",")}
func containerIndex(items []corev1.Container) map[string]corev1.Container {out:=map[string]corev1.Container{};for _,x:=range items{out[x.Name]=x};return out}
func sortedKeys(m map[string]struct{}) []string {k:=[]string{};for x:=range m{k=append(k,x)};sort.Strings(k);return k}
func addChange(c *[]Change,field,before,after string){if before!=after{*c=append(*c,Change{Field:field,Before:before,After:after})}}
func compareResources(c *[]Change,container,kind string,before,after corev1.ResourceList){
 keys:=map[string]struct{}{};for k:=range before{keys[string(k)]=struct{}{}};for k:=range after{keys[string(k)]=struct{}{}}
 for _,k:=range sortedKeys(keys){a,ok1:=before[corev1.ResourceName(k)];b,ok2:=after[corev1.ResourceName(k)];x,y:="<unset>","<unset>";if ok1{x=a.String()};if ok2{y=b.String()};addChange(c,fmt.Sprintf("%s.resources.%s.%s",container,kind,k),x,y)}
}
func compareEnv(c *[]Change,container string,before,after []corev1.EnvVar){
 l,r:=envIndex(before),envIndex(after);keys:=map[string]struct{}{};for k:=range l{keys[k]=struct{}{}};for k:=range r{keys[k]=struct{}{}}
 for _,k:=range sortedKeys(keys){a,ok1:=l[k];b,ok2:=r[k];field:=container+".env."+k
  switch{case !ok1:*c=append(*c,Change{Field:field,Before:"<absent>",After:"<present>"})
  case !ok2:*c=append(*c,Change{Field:field,Before:"<present>",After:"<absent>"})
  case !reflect.DeepEqual(a,b):x,y:=envRef(a),envRef(b);if x==""&&y==""{x,y="<redacted>","<redacted>"};addChange(c,field,x,y)}
 }
}
func envRef(v corev1.EnvVar) string {if v.ValueFrom==nil{return ""};switch{case v.ValueFrom.SecretKeyRef!=nil:return "secret/"+v.ValueFrom.SecretKeyRef.Name+"/"+v.ValueFrom.SecretKeyRef.Key;case v.ValueFrom.ConfigMapKeyRef!=nil:return "configMap/"+v.ValueFrom.ConfigMapKeyRef.Name+"/"+v.ValueFrom.ConfigMapKeyRef.Key;case v.ValueFrom.FieldRef!=nil:return "fieldRef/"+v.ValueFrom.FieldRef.FieldPath;case v.ValueFrom.ResourceFieldRef!=nil:return "resourceFieldRef/"+v.ValueFrom.ResourceFieldRef.Resource};return "valueFrom/changed"}
func envIndex(items []corev1.EnvVar) map[string]corev1.EnvVar {out:=map[string]corev1.EnvVar{};for _,x:=range items{out[x.Name]=x};return out}
func compareEnvFrom(c *[]Change,container string,before,after []corev1.EnvFromSource){
 if reflect.DeepEqual(before,after){return};format:=func(items []corev1.EnvFromSource)string{v:=[]string{};for _,x:=range items{if x.ConfigMapRef!=nil{v=append(v,"configMap/"+x.ConfigMapRef.Name)};if x.SecretRef!=nil{v=append(v,"secret/"+x.SecretRef.Name)}};sort.Strings(v);return strings.Join(v,",")};addChange(c,container+".envFrom",format(before),format(after))
}

func formatTolerations(items []corev1.Toleration) string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		operator := string(item.Operator)
		if operator == "" {
			operator = string(corev1.TolerationOpEqual)
		}
		value := item.Value
		if value == "" {
			value = "*"
		}
		effect := string(item.Effect)
		if effect == "" {
			effect = "*"
		}
		values = append(values, fmt.Sprintf("%s=%s:%s", item.Key, value, effect))
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func addStringMapChanges(changes *[]Change, prefix string, before, after map[string]string) {
 keys := map[string]struct{}{}
 for key := range before { keys[key] = struct{}{} }
 for key := range after { keys[key] = struct{}{} }
 for _, key := range sortedKeys(keys) {
  oldValue, oldOK := before[key]
  newValue, newOK := after[key]
  if oldOK == newOK && oldValue == newValue { continue }
  if isSensitiveAnnotation(key) {
   oldValue, newValue = "<redacted>", "<redacted>"
  } else if !oldOK {
   oldValue = "<absent>"
  } else if !newOK {
   newValue = "<absent>"
  }
  addChange(changes, prefix+"."+key, oldValue, newValue)
 }
}

func isSensitiveAnnotation(key string) bool {
 lower := strings.ToLower(key)
 for _, marker := range []string{"secret", "password", "token", "credential", "private-key", "authorization"} {
  if strings.Contains(lower, marker) { return true }
 }
 return false
}
