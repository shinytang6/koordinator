/*
 Copyright 2022 The Koordinator Authors.

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// MemoryQoS enables memory qos features.
type MemoryQoS struct {
	// memcg qos
	// If enabled, memcg qos will be set by the agent, where some fields are implicitly calculated from pod spec.
	// 1. `memory.min` := spec.requests.memory * minLimitFactor / 100 (use 0 if requests.memory is not set)
	// 2. `memory.low` := spec.requests.memory * lowLimitFactor / 100 (use 0 if requests.memory is not set)
	// 3. `memory.limit_in_bytes` := spec.limits.memory (set $node.allocatable.memory if limits.memory is not set)
	// 4. `memory.high` := memory.limit_in_bytes * throttlingFactor / 100 (use "max" if memory.high <= memory.min)
	// MinLimitPercent specifies the minLimitFactor percentage to calculate `memory.min`, which protects memory
	// from global reclamation when memory usage does not exceed the min limit.
	// Close: 0.
	// +kubebuilder:validation:Minimum=0
	MinLimitPercent *int64 `json:"minLimitPercent,omitempty"`
	// LowLimitPercent specifies the lowLimitFactor percentage to calculate `memory.low`, which TRIES BEST
	// protecting memory from global reclamation when memory usage does not exceed the low limit unless no unprotected
	// memcg can be reclaimed.
	// NOTE: `memory.low` should be larger than `memory.min`. If spec.requests.memory == spec.limits.memory,
	// pod `memory.low` and `memory.high` become invalid, while `memory.wmark_ratio` is still in effect.
	// Close: 0.
	// +kubebuilder:validation:Minimum=0
	LowLimitPercent *int64 `json:"lowLimitPercent,omitempty"`
	// ThrottlingPercent specifies the throttlingFactor percentage to calculate `memory.high` with pod
	// memory.limits or node allocatable memory, which triggers memcg direct reclamation when memory usage exceeds.
	// Lower the factor brings more heavier reclaim pressure.
	// Close: 0.
	// +kubebuilder:validation:Minimum=0
	ThrottlingPercent *int64 `json:"throttlingPercent,omitempty"`

	// wmark_ratio (Anolis OS required)
	// Async memory reclamation is triggered when cgroup memory usage exceeds `memory.wmark_high` and the reclamation
	// stops when usage is below `memory.wmark_low`. Basically,
	// `memory.wmark_high` := min(memory.high, memory.limit_in_bytes) * memory.wmark_scale_factor
	// `memory.wmark_low` := min(memory.high, memory.limit_in_bytes) * (memory.wmark_ratio - memory.wmark_scale_factor)
	// WmarkRatio specifies `memory.wmark_ratio` that help calculate `memory.wmark_high`, which triggers async
	// memory reclamation when memory usage exceeds.
	// Close: 0. Recommended: 95.
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:validation:Minimum=0
	WmarkRatio *int64 `json:"wmarkRatio,omitempty"`
	// WmarkScalePermill specifies `memory.wmark_scale_factor` that helps calculate `memory.wmark_low`, which
	// stops async memory reclamation when memory usage belows.
	// Close: 50. Recommended: 20.
	// +kubebuilder:validation:Maximum=1000
	// +kubebuilder:validation:Minimum=1
	WmarkScalePermill *int64 `json:"wmarkScalePermill,omitempty"`

	// wmark_min_adj (Anolis OS required)
	// WmarkMinAdj specifies `memory.wmark_min_adj` which adjusts per-memcg threshold for global memory
	// reclamation. Lower the factor brings later reclamation.
	// The adjustment uses different formula for different value range.
	// [-25, 0)：global_wmark_min' = global_wmark_min + (global_wmark_min - 0) * wmarkMinAdj
	// (0, 50]：global_wmark_min' = global_wmark_min + (global_wmark_low - global_wmark_min) * wmarkMinAdj
	// Close: [LSR:0, LS:0, BE:0]. Recommended: [LSR:-25, LS:-25, BE:50].
	// +kubebuilder:validation:Maximum=50
	// +kubebuilder:validation:Minimum=-25
	WmarkMinAdj *int64 `json:"wmarkMinAdj,omitempty"`

	// TODO: enhance the usages of oom priority and oom kill group
	PriorityEnable *int64 `json:"priorityEnable,omitempty"`
	Priority       *int64 `json:"priority,omitempty"`
	OomKillGroup   *int64 `json:"oomKillGroup,omitempty"`
}

type PodMemoryQoSPolicy string

const (
	// PodMemoryQoSPolicyDefault indicates pod inherits node-level config
	PodMemoryQoSPolicyDefault PodMemoryQoSPolicy = "default"
	// PodMemoryQoSPolicyNone indicates pod disables memory qos
	PodMemoryQoSPolicyNone PodMemoryQoSPolicy = "none"
	// PodMemoryQoSPolicyAuto indicates pod uses a recommended config
	PodMemoryQoSPolicyAuto PodMemoryQoSPolicy = "auto"
)

type PodMemoryQoSConfig struct {
	// Policy indicates the qos plan; use "default" if empty
	Policy    PodMemoryQoSPolicy `json:"policy,omitempty"`
	MemoryQoS `json:",inline"`
}

// MemoryQoSCfg stores node-level config of memory qos
type MemoryQoSCfg struct {
	// Enable indicates whether the memory qos is enabled (default: false).
	// This field is used for node-level control, while pod-level configuration is done with MemoryQoS and `Policy`
	// instead of an `Enable` option. Please view the differences between MemoryQoSCfg and PodMemoryQoSConfig structs.
	Enable    *bool `json:"enable,omitempty"`
	MemoryQoS `json:",inline"`
}

type ResourceQoS struct {
	MemoryQoS  *MemoryQoSCfg  `json:"memoryQoS,omitempty"`
	ResctrlQoS *ResctrlQoSCfg `json:"resctrlQoS,omitempty"`
}

type ResourceQoSStrategy struct {
	// ResourceQoS for LSR pods.
	LSR *ResourceQoS `json:"lsr,omitempty"`

	// ResourceQoS for LS pods.
	LS *ResourceQoS `json:"ls,omitempty"`

	// ResourceQoS for BE pods.
	BE *ResourceQoS `json:"be,omitempty"`

	// ResourceQoS for system pods
	System *ResourceQoS `json:"system,omitempty"`

	// ResourceQoS for root cgroup.
	CgroupRoot *ResourceQoS `json:"cgroupRoot,omitempty"`
}

type CPUSuppressPolicy string

const (
	CPUSetPolicy      CPUSuppressPolicy = "cpuset"
	CPUCfsQuotaPolicy CPUSuppressPolicy = "cfsQuota"
)

type ResourceThresholdStrategy struct {
	// whether the strategy is enabled, default = true
	// +kubebuilder:default=true
	Enable *bool `json:"enable,omitempty"`

	// cpu suppress threshold percentage (0,100), default = 65
	// +kubebuilder:default=65
	CPUSuppressThresholdPercent *int64 `json:"cpuSuppressThresholdPercent,omitempty"`

	// CPUSuppressPolicy
	CPUSuppressPolicy CPUSuppressPolicy `json:"cpuSuppressPolicy,omitempty"`

	// upper: memory evict threshold percentage (0,100), default = 70
	// +kubebuilder:default=70
	MemoryEvictThresholdPercent *int64 `json:"memoryEvictThresholdPercent,omitempty"`

	// lower: memory release util usage under MemoryEvictLowerPercent, default = MemoryEvictThresholdPercent - 2
	MemoryEvictLowerPercent *int64 `json:"memoryEvictLowerPercent,omitempty"`
}

// ResctrlQoSCfg stores node-level config of resctrl qos
type ResctrlQoSCfg struct {
	// Enable indicates whether the resctrl qos is enabled.
	Enable     *bool `json:"enable,omitempty"`
	ResctrlQoS `json:",inline"`
}

type ResctrlQoS struct {
	// LLC available range start for pods by percentage
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CATRangeStartPercent *int64 `json:"catRangeStartPercent,omitempty"`
	// LLC available range end for pods by percentage
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CATRangeEndPercent *int64 `json:"catRangeEndPercent,omitempty"`
	// MBA percent
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MBAPercent *int64 `json:"mbaPercent,omitempty"`
}

type CPUBurstPolicy string

const (
	// disable cpu burst policy
	CPUBurstNone CPUBurstPolicy = "none"
	// only enable cpu burst policy by setting cpu.cfs_burst_us
	CPUBurstOnly CPUBurstPolicy = "cpuBurstOnly"
	// only enable cfs quota burst policy by scale up cpu.cfs_quota_us if pod throttled
	CFSQuotaBurstOnly CPUBurstPolicy = "cfsQuotaBurstOnly"
	// enable both
	CPUBurstAuto CPUBurstPolicy = "auto"
)

type CPUBurstConfig struct {
	Policy CPUBurstPolicy `json:"policy,omitempty"`
	// cpu burst percentage for setting cpu.cfs_burst_us, legal range: [0, 10000], default as 1000 (1000%)
	// +kubebuilder:default=1000
	CPUBurstPercent *int64 `json:"cpuBurstPercent,omitempty"`
	// pod cfs quota scale up ceil percentage, default = 300 (300%)
	// +kubebuilder:default=300
	CFSQuotaBurstPercent *int64 `json:"cfsQuotaBurstPercent,omitempty"`
	// specifies a period of time for pod can use at burst, default = -1 (unlimited)
	// +kubebuilder:default=-1
	CFSQuotaBurstPeriodSeconds *int64 `json:"cfsQuotaBurstPeriodSeconds,omitempty"`
}

type CPUBurstStrategy struct {
	CPUBurstConfig `json:",inline"`
	// scale down cfs quota if node cpu overload, default = 50
	// +kubebuilder:default=50
	SharePoolThresholdPercent *int64 `json:"sharePoolThresholdPercent,omitempty"`
}

// NodeSLOSpec defines the desired state of NodeSLO
type NodeSLOSpec struct {
	// BE pods will be limited if node resource usage overload
	ResourceUsedThresholdWithBE *ResourceThresholdStrategy `json:"resourceUsedThresholdWithBE,omitempty"`
	// QoS config strategy for pods of different qos-class
	ResourceQoSStrategy *ResourceQoSStrategy `json:"resourceQoSStrategy,omitempty"`
	// CPU Burst Strategy
	CPUBurstStrategy *CPUBurstStrategy `json:"cpuBurstStrategy,omitempty"`
}

// NodeSLOStatus defines the observed state of NodeSLO
type NodeSLOStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// NodeSLO is the Schema for the nodeslos API
type NodeSLO struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeSLOSpec   `json:"spec,omitempty"`
	Status NodeSLOStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeSLOList contains a list of NodeSLO
type NodeSLOList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeSLO `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeSLO{}, &NodeSLOList{})
}
