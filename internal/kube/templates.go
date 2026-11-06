package kube

// Templates for creating resources, used by "+ Create" in the window and :create in
// the terminal. NAMESPACE is replaced with the target namespace. Generated once from
// the old templates.js; edit here.

import "strings"

// Template is one starting point.
type Template struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	YAML  string `json:"yaml"`
}

// Templates, in menu order.
var Templates = []Template{
	{"deployments.apps", "Deployment", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: NAMESPACE
  labels:
    app: my-app
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
        - name: app
          image: nginx:1.27          # the image to run
          ports:
            - containerPort: 80
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits: { memory: 128Mi }
          readinessProbe:
            httpGet: { path: /, port: 80 }
`},
	{"pods", "Pod", `apiVersion: v1
kind: Pod
metadata:
  name: my-pod
  namespace: NAMESPACE
  labels:
    app: my-pod
spec:
  containers:
    - name: main
      image: busybox:1.36          # the image to run
      command: ["sh", "-c", "echo hello && sleep 3600"]
      resources:
        requests: { cpu: 10m, memory: 16Mi }
        limits: { memory: 64Mi }
  restartPolicy: Never
`},
	{"services", "Service", `apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: NAMESPACE
spec:
  type: ClusterIP
  selector:
    app: my-app                     # pods with this label get the traffic
  ports:
    - name: http
      port: 80
      targetPort: 80
`},
	{"ingresses.networking.k8s.io", "Ingress", `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app
  namespace: NAMESPACE
spec:
  ingressClassName: alb             # see Network → Ingress Classes
  rules:
    - host: my-app.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service: { name: my-app, port: { number: 80 } }
`},
	{"configmaps", "ConfigMap", `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-config
  namespace: NAMESPACE
data:
  LOG_LEVEL: info
  app.yaml: |
    feature: true
`},
	{"secrets", "Secret", `apiVersion: v1
kind: Secret
metadata:
  name: my-secret
  namespace: NAMESPACE
type: Opaque
stringData:                         # plain text here; Kubernetes stores it encoded
  USERNAME: admin
  PASSWORD: change-me
`},
	{"jobs.batch", "Job", `apiVersion: batch/v1
kind: Job
metadata:
  name: my-job
  namespace: NAMESPACE
spec:
  backoffLimit: 2
  ttlSecondsAfterFinished: 3600     # cleaned up an hour after it finishes
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: job
          image: busybox:1.36
          command: ["sh", "-c", "echo done"]
`},
	{"cronjobs.batch", "CronJob", `apiVersion: batch/v1
kind: CronJob
metadata:
  name: my-cron
  namespace: NAMESPACE
spec:
  schedule: "0 * * * *"             # every hour
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: job
              image: busybox:1.36
              command: ["sh", "-c", "date"]
`},
	{"statefulsets.apps", "StatefulSet", `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-db
  namespace: NAMESPACE
spec:
  serviceName: my-db
  replicas: 1
  selector:
    matchLabels: { app: my-db }
  template:
    metadata:
      labels: { app: my-db }
    spec:
      containers:
        - name: db
          image: redis:7
          ports: [{ containerPort: 6379 }]
          volumeMounts: [{ name: data, mountPath: /data }]
  volumeClaimTemplates:
    - metadata: { name: data }
      spec:
        accessModes: [ReadWriteOnce]
        resources: { requests: { storage: 1Gi } }
`},
	{"daemonsets.apps", "DaemonSet", `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: my-agent
  namespace: NAMESPACE
spec:
  selector:
    matchLabels: { app: my-agent }
  template:
    metadata:
      labels: { app: my-agent }
    spec:
      containers:
        - name: agent
          image: busybox:1.36
          command: ["sh", "-c", "sleep infinity"]
          resources: { requests: { cpu: 10m, memory: 16Mi } }
`},
	{"persistentvolumeclaims", "PersistentVolumeClaim", `apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-data
  namespace: NAMESPACE
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests: { storage: 1Gi }
  # storageClassName: gp3           # see Storage → Storage Classes
`},
	{"horizontalpodautoscalers.autoscaling", "HPA", `apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: my-app
  namespace: NAMESPACE
spec:
  scaleTargetRef: { apiVersion: apps/v1, kind: Deployment, name: my-app }
  minReplicas: 1
  maxReplicas: 5
  metrics:
    - type: Resource
      resource: { name: cpu, target: { type: Utilization, averageUtilization: 70 } }
`},
	{"poddisruptionbudgets.policy", "PodDisruptionBudget", `apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: my-app
  namespace: NAMESPACE
spec:
  maxUnavailable: 1                 # leave room for node drains
  selector:
    matchLabels: { app: my-app }
`},
	{"networkpolicies.networking.k8s.io", "NetworkPolicy", `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-same-namespace
  namespace: NAMESPACE
spec:
  podSelector: {}
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector: {}
`},
	{"serviceaccounts", "ServiceAccount", `apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-sa
  namespace: NAMESPACE
  # annotations:
  #   eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/my-role
`},
	{"roles.rbac.authorization.k8s.io", "Role", `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: pod-reader
  namespace: NAMESPACE
rules:
  - apiGroups: [""]
    resources: [pods, pods/log]
    verbs: [get, list, watch]
`},
	{"rolebindings.rbac.authorization.k8s.io", "RoleBinding", `apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: pod-reader
  namespace: NAMESPACE
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: pod-reader }
subjects:
  - kind: ServiceAccount
    name: my-sa
    namespace: NAMESPACE
`},
	{"resourcequotas", "ResourceQuota", `apiVersion: v1
kind: ResourceQuota
metadata:
  name: compute
  namespace: NAMESPACE
spec:
  hard:
    requests.cpu: "4"
    requests.memory: 8Gi
    pods: "50"
`},
	{"namespaces", "Namespace", `apiVersion: v1
kind: Namespace
metadata:
  name: my-namespace
`},
}

// TemplateFor returns the template for a kind id (or a skeleton built from the kind),
// with the namespace filled in.
func TemplateFor(k Kind, namespace string) string {
	if namespace == "" {
		namespace = "default"
	}
	for _, t := range Templates {
		if t.ID == k.ID() {
			return strings.ReplaceAll(t.YAML, "NAMESPACE", namespace)
		}
	}
	if k.Kind == "" {
		return strings.ReplaceAll(Templates[0].YAML, "NAMESPACE", namespace)
	}
	api := k.Version
	if k.Group != "" {
		api = k.Group + "/" + k.Version
	}
	s := "apiVersion: " + api + "\nkind: " + k.Kind + "\nmetadata:\n  name: my-" + strings.ToLower(k.Kind) + "\n"
	if k.Namespaced {
		s += "  namespace: " + namespace + "\n"
	}
	return s + "spec: {}\n"
}
