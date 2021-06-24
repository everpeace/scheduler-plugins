/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1beta1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulerconfig "k8s.io/kube-scheduler/config/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CoschedulingArgs defines the scheduling parameters for Coscheduling plugin.
type CoschedulingArgs struct {
	metav1.TypeMeta `json:",inline"`

	// PermitWaitingTime is the wait timeout in seconds.
	PermitWaitingTimeSeconds *int64 `json:"permitWaitingTimeSeconds,omitempty"`
	// DeniedPGExpirationTimeSeconds is the expiration time of the denied podgroup store.
	DeniedPGExpirationTimeSeconds *int64 `json:"deniedPGExpirationTimeSeconds,omitempty"`
	// KubeMaster is the url of api-server
	KubeMaster *string `json:"kubeMaster,omitempty"`
	// KubeConfigPath for scheduler
	KubeConfigPath *string `json:"kubeConfigPath,omitempty"`
}

// ModeType is a type "string".
type ModeType string

const (
	// Least is the string "Least".
	Least ModeType = "Least"
	// Most is the string "Most".
	Most ModeType = "Most"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeResourcesAllocatableArgs holds arguments used to configure NodeResourcesAllocatable plugin.
type NodeResourcesAllocatableArgs struct {
	metav1.TypeMeta `json:",inline"`

	// Resources to be considered when scoring.
	// Allowed weights start from 1.
	// An example resource set might include "cpu" (millicores) and "memory" (bytes)
	// with weights of 1<<20 and 1 respectfully. That would mean 1 MiB has equivalent
	// weight as 1 millicore.
	Resources []schedulerconfig.ResourceSpec `json:"resources,omitempty"`

	// Whether to prioritize nodes with least or most allocatable resources.
	Mode ModeType `json:"mode,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CapacitySchedulingArgs defines the scheduling parameters for CapacityScheduling plugin.
type CapacitySchedulingArgs struct {
	metav1.TypeMeta `json:",inline"`

	// KubeConfigPath is the path of kubeconfig.
	KubeConfigPath *string `json:"kubeConfigPath,omitempty"`
}

// MetricProviderType is a "string" type.
type MetricProviderType string

const (
	KubernetesMetricsServer MetricProviderType = "KubernetesMetricsServer"
	Prometheus              MetricProviderType = "Prometheus"
	SignalFx                MetricProviderType = "SignalFx"
)

// Denote the spec of the metric provider
type MetricProviderSpec struct {
	// Types of the metric provider
	Type MetricProviderType `json:"type,omitempty"`
	// The address of the metric provider
	Address *string `json:"address,omitempty"`
	// The authentication token of the metric provider
	Token *string `json:"token,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:defaulter-gen=true

// TargetLoadPackingArgs holds arguments used to configure TargetLoadPacking plugin.
type TargetLoadPackingArgs struct {
	metav1.TypeMeta `json:",inline"`

	// Default requests to use for best effort QoS
	DefaultRequests v1.ResourceList `json:"defaultRequests,omitempty"`
	// Default requests multiplier for busrtable QoS
	DefaultRequestsMultiplier *string `json:"defaultRequestsMultiplier,omitempty"`
	// Node target CPU Utilization for bin packing
	TargetUtilization *int64 `json:"targetUtilization,omitempty"`
	// Specify the metric provider type, address and token using MetricProviderSpec
	MetricProvider MetricProviderSpec `json:"metricProvider,omitempty"`
	// Address of load watcher service
	WatcherAddress *string `json:"watcherAddress,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:defaulter-gen=true

// LoadVariationRiskBalancingArgs holds arguments used to configure LoadVariationRiskBalancing plugin.
type LoadVariationRiskBalancingArgs struct {
	metav1.TypeMeta `json:",inline"`

	// Metric Provider specification when using load watcher as library
	MetricProvider MetricProviderSpec `json:"metricProvider,omitempty"`
	// Address of load watcher service
	WatcherAddress *string `json:"watcherAddress,omitempty"`
	// Multiplier of standard deviation in risk value
	SafeVarianceMargin *float64 `json:"safeVarianceMargin,omitempty"`
	// Root power of standard deviation in risk value
	SafeVarianceSensitivity *float64 `json:"safeVarianceSensitivity,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeResourceTopologyMatchArgs holds arguments used to configure the NodeResourceTopologyMatch plugin
type NodeResourceTopologyMatchArgs struct {
	metav1.TypeMeta `json:",inline"`

	KubeConfigPath *string  `json:"kubeconfigpath,omitempty"`
	MasterOverride *string  `json:"masteroverride,omitempty"`
	Namespaces     []string `json:"namespaces,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PreemptionTolerationArgs holds arguments used to configure PreemptionToleration plugin.
// Note: This is identical to DefaultPluginArgs.
type PreemptionTolerationArgs struct {
	metav1.TypeMeta `json:",inline"`

	// MinCandidateNodesPercentage is the minimum number of candidates to
	// shortlist when dry running preemption as a percentage of number of nodes.
	// Must be in the range [0, 100]. Defaults to 10% of the cluster size if
	// unspecified.
	MinCandidateNodesPercentage *int32 `json:"minCandidateNodesPercentage,omitempty"`
	// MinCandidateNodesAbsolute is the absolute minimum number of candidates to
	// shortlist. The likely number of candidates enumerated for dry running
	// preemption is given by the formula:
	// numCandidates = max(numNodes * minCandidateNodesPercentage, minCandidateNodesAbsolute)
	// We say "likely" because there are other factors such as PDB violations
	// that play a role in the number of candidates shortlisted. Must be at least
	// 0 nodes. Defaults to 100 nodes if unspecified.
	MinCandidateNodesAbsolute *int32 `json:"minCandidateNodesAbsolute,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PreemptionToleration holds preemption toleration configuration used by PreemptionToleration plugin. This configuration will be annotated to PriorityClass resources.
type PreemptionToleration struct {
	metav1.TypeMeta `json:",inline"`

	// MinimumPreemptablePriority specifies the minimum priority value that can preempt this priority class. The default value is the value of annotated priority class.
	MinimumPreemptablePriority *int32 `json:"minimumPreemptablePriority,omitempty"`

	// TolerationSeconds specified how long this priority class can tolerate preemption by priorities lower than MinimumPreemptablePriority.  The default value is forever.  Duration means the duration from the pod being scheduled to some node. This value affects only to scheduled pods (no effect on nominated nodes).
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}
