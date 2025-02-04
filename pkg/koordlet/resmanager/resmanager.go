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

package resmanager

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/watch"
	clientset "k8s.io/client-go/kubernetes"
	clientcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"

	slov1alpha1 "github.com/koordinator-sh/koordinator/apis/slo/v1alpha1"
	koordclientset "github.com/koordinator-sh/koordinator/pkg/client/clientset/versioned"
	slolisterv1alpha1 "github.com/koordinator-sh/koordinator/pkg/client/listers/slo/v1alpha1"
	"github.com/koordinator-sh/koordinator/pkg/features"
	"github.com/koordinator-sh/koordinator/pkg/koordlet/audit"
	"github.com/koordinator-sh/koordinator/pkg/koordlet/metriccache"
	"github.com/koordinator-sh/koordinator/pkg/koordlet/metrics"
	"github.com/koordinator-sh/koordinator/pkg/koordlet/statesinformer"
	"github.com/koordinator-sh/koordinator/pkg/runtime"
	expireCache "github.com/koordinator-sh/koordinator/pkg/tools/cache"
	"github.com/koordinator-sh/koordinator/pkg/util"
)

const (
	evictPodSuccess = "evictPodSuccess"
	evictPodFail    = "evictPodFail"
)

type ResManager interface {
	Run(stopCh <-chan struct{}) error
}

type resmanager struct {
	config                        *Config
	collectResUsedIntervalSeconds int64
	nodeName                      string
	schema                        *apiruntime.Scheme
	statesInformer                statesinformer.StatesInformer
	metricCache                   metriccache.MetricCache
	podsEvicted                   *expireCache.Cache
	nodeSLOInformer               cache.SharedIndexInformer
	nodeSLOLister                 slolisterv1alpha1.NodeSLOLister
	kubeClient                    clientset.Interface
	eventRecorder                 record.EventRecorder

	// nodeSLO stores the latest nodeSLO object for the current node
	nodeSLO        *slov1alpha1.NodeSLO
	nodeSLORWMutex sync.RWMutex
}

func newNodeSLOInformer(client koordclientset.Interface, nodeName string) cache.SharedIndexInformer {
	tweakListOptionFunc := func(opt *metav1.ListOptions) {
		opt.FieldSelector = "metadata.name=" + nodeName
	}
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (apiruntime.Object, error) {
				tweakListOptionFunc(&options)
				return client.SloV1alpha1().NodeSLOs().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				tweakListOptionFunc(&options)
				return client.SloV1alpha1().NodeSLOs().Watch(context.TODO(), options)
			},
		},
		&slov1alpha1.NodeSLO{},
		time.Hour*12,
		cache.Indexers{},
	)
}

// mergeNodeSLOSpec merges nodeSLO with default config; ensure use the function with a RWMutex
func (r *resmanager) mergeNodeSLOSpec(nodeSLO *slov1alpha1.NodeSLO) {
	if r.nodeSLO == nil || nodeSLO == nil {
		klog.Errorf("failed to merge with nil nodeSLO, old: %v, new: %v", r.nodeSLO, nodeSLO)
		return
	}

	// merge ResourceUsedThresholdWithBE individually for nil-ResourceUsedThresholdWithBE case
	mergedResourceUsedThresholdWithBESpec := mergeSLOSpecResourceUsedThresholdWithBE(util.DefaultNodeSLOSpecConfig().ResourceUsedThresholdWithBE,
		nodeSLO.Spec.ResourceUsedThresholdWithBE)
	if mergedResourceUsedThresholdWithBESpec != nil {
		r.nodeSLO.Spec.ResourceUsedThresholdWithBE = mergedResourceUsedThresholdWithBESpec
	}

	// merge ResourceQoSStrategy
	mergedResourceQoSStrategySpec := mergeSLOSpecResourceQoSStrategy(util.DefaultNodeSLOSpecConfig().ResourceQoSStrategy,
		nodeSLO.Spec.ResourceQoSStrategy)
	mergeNoneResourceQoSIfDisabled(mergedResourceQoSStrategySpec)
	if mergedResourceQoSStrategySpec != nil {
		r.nodeSLO.Spec.ResourceQoSStrategy = mergedResourceQoSStrategySpec
	}

	// merge CPUBurstStrategy
	mergedCPUBurstStrategySpec := mergeSLOSpecCPUBurstStrategy(util.DefaultNodeSLOSpecConfig().CPUBurstStrategy,
		nodeSLO.Spec.CPUBurstStrategy)
	if mergedCPUBurstStrategySpec != nil {
		r.nodeSLO.Spec.CPUBurstStrategy = mergedCPUBurstStrategySpec
	}
}

func (r *resmanager) createNodeSLO(nodeSLO *slov1alpha1.NodeSLO) {
	r.nodeSLORWMutex.Lock()
	defer r.nodeSLORWMutex.Unlock()

	oldNodeSLOStr := util.DumpJSON(r.nodeSLO)

	r.nodeSLO = nodeSLO.DeepCopy()
	r.nodeSLO.Spec = nodeSLO.Spec

	// merge nodeSLO spec with the default config
	r.mergeNodeSLOSpec(nodeSLO)

	newNodeSLOStr := util.DumpJSON(r.nodeSLO)
	klog.Infof("update nodeSLO content: old %s, new %s", oldNodeSLOStr, newNodeSLOStr)
}

func (r *resmanager) getNodeSLOCopy() *slov1alpha1.NodeSLO {
	r.nodeSLORWMutex.Lock()
	defer r.nodeSLORWMutex.Unlock()

	if r.nodeSLO == nil {
		return nil
	}
	nodeSLOCopy := r.nodeSLO.DeepCopy()
	return nodeSLOCopy
}

func (r *resmanager) updateNodeSLOSpec(nodeSLO *slov1alpha1.NodeSLO) {
	r.nodeSLORWMutex.Lock()
	defer r.nodeSLORWMutex.Unlock()

	oldNodeSLOStr := util.DumpJSON(r.nodeSLO)

	r.nodeSLO.Spec = nodeSLO.Spec

	// merge nodeSLO spec with the default config
	r.mergeNodeSLOSpec(nodeSLO)

	newNodeSLOStr := util.DumpJSON(r.nodeSLO)
	klog.Infof("update nodeSLO content: old %s, new %s", oldNodeSLOStr, newNodeSLOStr)
}

func NewResManager(cfg *Config, schema *apiruntime.Scheme, kubeClient clientset.Interface, crdClient *koordclientset.Clientset, nodeName string,
	statesInformer statesinformer.StatesInformer, metricCache metriccache.MetricCache, collectResUsedIntervalSeconds int64) ResManager {
	informer := newNodeSLOInformer(crdClient, nodeName)

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartRecordingToSink(&clientcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(schema, corev1.EventSource{Component: "slo-agent-reporter", Host: nodeName})

	r := &resmanager{
		config:                        cfg,
		nodeName:                      nodeName,
		schema:                        schema,
		statesInformer:                statesInformer,
		metricCache:                   metricCache,
		podsEvicted:                   expireCache.NewCacheDefault(),
		nodeSLOInformer:               informer,
		nodeSLOLister:                 slolisterv1alpha1.NewNodeSLOLister(informer.GetIndexer()),
		kubeClient:                    kubeClient,
		eventRecorder:                 recorder,
		collectResUsedIntervalSeconds: collectResUsedIntervalSeconds,
	}
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			nodeSLO, ok := obj.(*slov1alpha1.NodeSLO)
			if ok {
				r.createNodeSLO(nodeSLO)
				klog.Infof("create NodeSLO %v", nodeSLO)
			} else {
				klog.Errorf("node slo informer add func parse nodeSLO failed")
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldNodeSLO, oldOK := oldObj.(*slov1alpha1.NodeSLO)
			newNodeSLO, newOK := newObj.(*slov1alpha1.NodeSLO)
			if !oldOK || !newOK {
				klog.Errorf("unable to convert object to *slov1alpha1.NodeSLO, old %T, new %T", oldObj, newObj)
				return
			}
			if reflect.DeepEqual(oldNodeSLO.Spec, newNodeSLO.Spec) {
				klog.V(5).Infof("find NodeSLO spec %s has not changed", newNodeSLO.Name)
				return
			}
			klog.Infof("update NodeSLO spec %v", newNodeSLO.Spec)
			r.updateNodeSLOSpec(newNodeSLO)
		},
	})

	return r
}

// isFeatureDisabled returns whether the featuregate is disabled by nodeSLO config
func isFeatureDisabled(nodeSLO *slov1alpha1.NodeSLO, feature featuregate.Feature) (bool, error) {
	if nodeSLO == nil || nodeSLO.Spec == (slov1alpha1.NodeSLOSpec{}) {
		return true, fmt.Errorf("cannot parse feature config for invalid nodeSLO %v", nodeSLO)
	}

	spec := nodeSLO.Spec
	switch feature {
	case features.BECPUSuppress, features.BEMemoryEvict:
		if spec.ResourceUsedThresholdWithBE == nil || spec.ResourceUsedThresholdWithBE.Enable == nil {
			return true, fmt.Errorf("cannot parse feature config for invalid nodeSLO %v", nodeSLO)
		}
		return !(*spec.ResourceUsedThresholdWithBE.Enable), nil
	default:
		return true, fmt.Errorf("cannot parse feature config for unsupported feature %s", feature)
	}
}

func (r *resmanager) Run(stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	klog.Info("Starting resmanager")

	r.podsEvicted.Run(stopCh)

	klog.Infof("starting informer for NodeSLO")
	go r.nodeSLOInformer.Run(stopCh)
	if !cache.WaitForCacheSync(stopCh, r.nodeSLOInformer.HasSynced) {
		return fmt.Errorf("time out waiting for node slo caches to sync")
	}

	if !cache.WaitForCacheSync(stopCh, r.statesInformer.HasSynced) {
		return fmt.Errorf("time out waiting for kubelet meta service caches to sync")
	}
	if !cache.WaitForCacheSync(stopCh, r.hasSynced) {
		return fmt.Errorf("time out waiting for sync NodeSLO")
	}

	util.RunFeature(r.reconcileBECgroup, []featuregate.Feature{features.BECgroupReconcile}, r.config.ReconcileIntervalSeconds, stopCh)

	cgroupResourceReconcile := NewCgroupResourcesReconcile(r)
	util.RunFeatureWithInit(func() error { return cgroupResourceReconcile.RunInit(stopCh) }, cgroupResourceReconcile.reconcile,
		[]featuregate.Feature{features.CgroupReconcile}, r.config.ReconcileIntervalSeconds, stopCh)

	cpuSuppress := NewCPUSuppress(r)
	util.RunFeature(cpuSuppress.suppressBECPU, []featuregate.Feature{features.BECPUSuppress}, r.config.CPUSuppressIntervalSeconds, stopCh)

	cpuBurst := NewCPUBurst(r)
	util.RunFeatureWithInit(func() error { return cpuBurst.init(stopCh) }, cpuBurst.start,
		[]featuregate.Feature{features.CPUBurst}, r.config.ReconcileIntervalSeconds, stopCh)

	memoryEvictor := NewMemoryEvictor(r)
	util.RunFeature(memoryEvictor.memoryEvict, []featuregate.Feature{features.BEMemoryEvict}, r.config.MemoryEvictIntervalSeconds, stopCh)

	rdtResCtrl := NewResctrlReconcile(r)
	util.RunFeatureWithInit(func() error { return rdtResCtrl.RunInit(stopCh) }, rdtResCtrl.reconcile,
		[]featuregate.Feature{features.RdtResctrl}, r.config.ReconcileIntervalSeconds, stopCh)

	klog.Info("Starting resmanager successfully")
	<-stopCh
	klog.Info("shutting down resmanager")
	return nil
}

func (r *resmanager) hasSynced() bool {
	r.nodeSLORWMutex.Lock()
	defer r.nodeSLORWMutex.Unlock()

	return r.nodeSLO != nil && r.nodeSLO.Spec.ResourceUsedThresholdWithBE != nil
}

func (r *resmanager) evictPodsIfNotEvicted(evictPods []*corev1.Pod, node *corev1.Node, reason string, message string) {
	for _, evictPod := range evictPods {
		r.evictPodIfNotEvicted(evictPod, node, reason, message)
	}
}

func (r *resmanager) evictPodIfNotEvicted(evictPod *corev1.Pod, node *corev1.Node, reason string, message string) {
	_, evicted := r.podsEvicted.Get(string(evictPod.UID))
	if evicted {
		klog.V(5).Infof("Pod has been evicted! podID: %v, evict reason: %s", evictPod.UID, reason)
		return
	}
	success := r.evictPod(evictPod, node, reason, message)
	if success {
		_ = r.podsEvicted.SetDefault(string(evictPod.UID), evictPod.UID)
	}
}

func (r *resmanager) evictPod(evictPod *corev1.Pod, node *corev1.Node, reason string, message string) bool {
	podEvictMessage := fmt.Sprintf("evict Pod:%s, reason: %s, message: %v", evictPod.Name, reason, message)
	_ = audit.V(0).Pod(evictPod.Namespace, evictPod.Name).Reason(reason).Message(message).Do()
	podEvict := policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      evictPod.Name,
			Namespace: evictPod.Namespace,
		},
	}

	if err := r.kubeClient.CoreV1().Pods(evictPod.Namespace).EvictV1(context.TODO(), &podEvict); err == nil {
		r.eventRecorder.Eventf(node, corev1.EventTypeWarning, evictPodSuccess, podEvictMessage)
		metrics.RecordPodEviction(reason)
		klog.Infof("evict pod %v/%v success, reason: %v", evictPod.Namespace, evictPod.Name, reason)
		return true
	} else if !errors.IsNotFound(err) {
		r.eventRecorder.Eventf(node, corev1.EventTypeWarning, evictPodFail, podEvictMessage)
		klog.Errorf("evict pod %v/%v failed, reason: %v, error: %v", evictPod.Namespace, evictPod.Name, reason, err)
		return false
	}
	return true
}

// killContainers kills containers inside the pod
func killContainers(pod *corev1.Pod, message string) {
	for _, container := range pod.Spec.Containers {
		containerID, containerStatus, err := util.FindContainerIdAndStatusByName(&pod.Status, container.Name)
		if err != nil {
			klog.Errorf("failed to find container id and status, error: %v", err)
			return
		}

		if containerStatus == nil || containerStatus.State.Running == nil {
			return
		}

		if containerID != "" {
			runtimeType, _, _ := util.ParseContainerId(containerStatus.ContainerID)
			runtimeHandler, err := runtime.GetRuntimeHandler(runtimeType)
			if err != nil || runtimeHandler == nil {
				klog.Errorf("%s, kill container(%s) error! GetRuntimeHandler fail! error: %v", message, containerStatus.ContainerID, err)
				continue
			}
			if err := runtimeHandler.StopContainer(containerID, 0); err != nil {
				klog.Errorf("%s, stop container error! error: %v", message, err)
			}
		} else {
			klog.Warningf("%s, get container ID failed, pod %s/%s containerName %s status: %v", message, pod.Namespace, pod.Name, container.Name, pod.Status.ContainerStatuses)
		}
	}
}
