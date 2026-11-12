package timeline

import (
 "context"
 "strings"
 "testing"
 "time"

 appsv1 "k8s.io/api/apps/v1"
 corev1 "k8s.io/api/core/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/types"
 "k8s.io/apimachinery/pkg/util/intstr"
 "k8s.io/client-go/kubernetes/fake"
)

func TestCompareTemplatesDetectsProbeCommandPortsAndInitContainer(t *testing.T) {
 old:=&appsv1.ReplicaSet{Spec:appsv1.ReplicaSetSpec{Template:corev1.PodTemplateSpec{Spec:corev1.PodSpec{
  Containers:[]corev1.Container{{Name:"api",Command:[]string{"serve"},Ports:[]corev1.ContainerPort{{ContainerPort:8080}},ReadinessProbe:&corev1.Probe{ProbeHandler:corev1.ProbeHandler{HTTPGet:&corev1.HTTPGetAction{Path:"/ready",Port:intstr.FromInt(8080)}}}}},
  InitContainers:[]corev1.Container{{Name:"migrate",Image:"migrate:v1"}},
 }}}}
 next:=old.DeepCopy()
 next.Spec.Template.Spec.Containers[0].Command=[]string{"serve","--password=do-not-print"}
 next.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort=9090
 next.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path="/health"
 next.Spec.Template.Spec.InitContainers[0].Image="migrate:v2"
 changes:=compareTemplates(old,next)
 for _,field:=range []string{"containers.api.command","containers.api.ports","containers.api.readinessProbe","initContainers.migrate.image"} {
  found:=false;for _,change:=range changes{if change.Field==field{found=true}}
  if !found{t.Errorf("missing %s diff: %+v",field,changes)}
 }
 for _,change:=range changes{if strings.Contains(change.Before,"password")||strings.Contains(change.After,"password"){t.Fatalf("command leaked: %+v",change)}}
}

func TestCompareEnvShowsReferenceNamesNotValues(t *testing.T) {
 var changes []Change
 old:=[]corev1.EnvVar{{Name:"DB_PASSWORD",ValueFrom:&corev1.EnvVarSource{SecretKeyRef:&corev1.SecretKeySelector{LocalObjectReference:corev1.LocalObjectReference{Name:"db-old"},Key:"PASSWORD"}}}}
 next:=[]corev1.EnvVar{{Name:"DB_PASSWORD",ValueFrom:&corev1.EnvVarSource{SecretKeyRef:&corev1.SecretKeySelector{LocalObjectReference:corev1.LocalObjectReference{Name:"db-new"},Key:"PASSWORD"}}}}
 compareEnv(&changes,"containers.api",old,next)
 if len(changes)!=1||changes[0].Before!="secret/db-old/PASSWORD"||changes[0].After!="secret/db-new/PASSWORD"{t.Fatalf("unexpected reference diff: %+v",changes)}
}

func TestAnalyzeDeploymentRollbackTimestampAndCurrentMarker(t *testing.T) {
 controller:=true
 oldTime:=metav1.NewTime(time.Date(2026,1,1,10,0,0,0,time.UTC))
 newTime:=metav1.NewTime(time.Date(2026,1,2,10,0,0,0,time.UTC))
 deployment:=&appsv1.Deployment{ObjectMeta:metav1.ObjectMeta{Name:"orders",Namespace:"prod",UID:types.UID("dep"),Annotations:map[string]string{revisionAnnotation:"3"}},Spec:appsv1.DeploymentSpec{Selector:&metav1.LabelSelector{MatchLabels:map[string]string{"app":"orders"}}}}
 makeRS:=func(name,rev string,created metav1.Time)*appsv1.ReplicaSet{return &appsv1.ReplicaSet{ObjectMeta:metav1.ObjectMeta{Name:name,Namespace:"prod",CreationTimestamp:created,Labels:map[string]string{"app":"orders"},Annotations:map[string]string{revisionAnnotation:rev},OwnerReferences:[]metav1.OwnerReference{{Kind:"Deployment",Name:"orders",UID:deployment.UID,Controller:&controller}}}}}
 client:=fake.NewSimpleClientset(deployment,makeRS("new","2",newTime),makeRS("old","3",oldTime))
 report,err:=(&Analyzer{Client:client,Now:func()time.Time{return newTime.Add(time.Hour)}}).AnalyzeDeployment(context.Background(),"prod","orders")
 if err!=nil{t.Fatal(err)}
 if len(report.Events)!=2{t.Fatalf("events=%d",len(report.Events))}
 if !report.Events[1].Current{t.Fatal("current revision not marked")}
 if report.Events[1].Timestamp != nil||!strings.Contains(report.Events[1].Details,"rollback likely"){t.Fatalf("expected honest rollback timestamp: %+v",report.Events[1])}
}

func TestAnalyzeDeploymentRejectsMissingSelector(t *testing.T) {
 client:=fake.NewSimpleClientset(&appsv1.Deployment{ObjectMeta:metav1.ObjectMeta{Name:"bad",Namespace:"default",UID:types.UID("d")}})
 if _,err:=(&Analyzer{Client:client}).AnalyzeDeployment(context.Background(),"default","bad");err==nil{t.Fatal("expected selector error")}
}
