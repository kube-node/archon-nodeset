/*
Copyright 2017 The Archon Authors.

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

package nodeset

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/kube-node/nodeset/pkg/client/clientset_v1alpha1"
	"github.com/kube-node/nodeset/pkg/client/clientset_v1alpha1/scheme"
	"github.com/kube-node/nodeset/pkg/nodeset/v1alpha1"

	archonclientset "kubeup.com/archon/pkg/clientset"
	"kubeup.com/archon/pkg/cluster"

	"github.com/golang/glog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pkg_runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	watch "k8s.io/apimachinery/pkg/watch"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/util/metrics"
)

const (
	// We'll attempt to recompute the required replicas of all NodeSets
	// that have fulfilled their expectations at least this often. This recomputation
	// happens based on contents in local node storage.
	FullControllerResyncPeriod = 30 * time.Second

	// The number of times we retry updating a NodeSet's status.
	statusUpdateRetries = 1

	nodesetWorkers = 3
)

func getNSKind() schema.GroupVersionKind {
	return cluster.SchemeGroupVersion.WithKind("NodeSet")
}

// NodeSetController syncs NodeSets to InstanceGroups in a one-to-one relationship.
// It syncs both ways:
// - Full sync from NodeSet to IG
// - Partial sync from IG status to NodeSet status
type NodeSetController struct {
	kubeClient    archonclientset.Interface
	nodesetClient clientset_v1alpha1.Interface

	instanceGroupNamespace string

	// To allow injection of syncNodeSet for testing.
	syncHandler func(igKey string) error

	instanceGroupIndexer    cache.Indexer
	nodesetIndexer          cache.Indexer
	instanceGroupController cache.Controller
	nodesetController       cache.Controller

	// Controllers that need to be synced
	nodesetQueue       workqueue.RateLimitingInterface
	instanceGroupQueue workqueue.RateLimitingInterface
}

// NewNodeSetController configures a nodeset controller with the specified event recorder
func NewNodeSetController(instanceGroupNamespace string, kubeClient archonclientset.Interface, nodesetClient clientset_v1alpha1.Interface) *NodeSetController {
	if kubeClient != nil && kubeClient.Core().RESTClient().GetRateLimiter() != nil {
		metrics.RegisterMetricAndTrackRateLimiterUsage("archon_nodeset_controller", kubeClient.Core().RESTClient().GetRateLimiter())
	}
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.Core().RESTClient()).Events("")})

	nsc := &NodeSetController{
		kubeClient:             kubeClient,
		nodesetClient:          nodesetClient,
		instanceGroupNamespace: instanceGroupNamespace,
		nodesetQueue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "nodeset"),
	}

	nsc.instanceGroupIndexer, nsc.instanceGroupController = cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (pkg_runtime.Object, error) {
				return nsc.kubeClient.Archon().InstanceGroups(nsc.instanceGroupNamespace).List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return nsc.kubeClient.Archon().InstanceGroups(nsc.instanceGroupNamespace).Watch(options)
			},
		},
		&cluster.InstanceGroup{},
		FullControllerResyncPeriod,
		cache.ResourceEventHandlerFuncs{
			// Ignore add/delete of InstanceGroup
			UpdateFunc: nsc.updateInstanceGroup,
		},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)

	nsc.nodesetIndexer, nsc.nodesetController = cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (pkg_runtime.Object, error) {
				return nsc.nodesetClient.NodesetV1alpha1().NodeSets().List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return nsc.nodesetClient.NodesetV1alpha1().NodeSets().Watch(options)
			},
		},
		&v1alpha1.NodeSet{},
		FullControllerResyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    nsc.enqueueNodeSet,
			UpdateFunc: nsc.updateNodeSet, // TODO: check
			DeleteFunc: nsc.enqueueNodeSet,
		},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)

	nsc.syncHandler = nsc.syncNodeSet
	return nsc
}

// Run begins watching and syncing.
func (nsc *NodeSetController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer nsc.nodesetQueue.ShutDown()

	glog.Infof("Starting NodeSet controller")

	go nsc.instanceGroupController.Run(wait.NeverStop)
	go nsc.nodesetController.Run(wait.NeverStop)

	if !cache.WaitForCacheSync(stopCh, nsc.instanceGroupController.HasSynced) {
		return
	}

	if !cache.WaitForCacheSync(stopCh, nsc.nodesetController.HasSynced) {
		return
	}

	for i := 0; i < nodesetWorkers; i++ {
		go wait.Until(nsc.nodesetWorker, time.Second, stopCh)
	}

	<-stopCh
	glog.Infof("Shutting down NodeSet Controller")
}

func (nsc *NodeSetController) updateInstanceGroup(oldObj, curObj interface{}) {
	cur := curObj.(*cluster.InstanceGroup)
	old := oldObj.(*cluster.InstanceGroup)
	if cur.ResourceVersion == old.ResourceVersion {
		// Periodic resync will send update events for all known nodes.
		// Two different versions of the same node will always have different RVs.
		return
	}

	// Compare Status
	if reflect.DeepEqual(cur.Status, old.Status) {
		// We only sync status.
		return
	}

	glog.V(4).Infof("InstanceGroup %s updated, objectMeta %+v -> %+v.", cur.Name, old.ObjectMeta, cur.ObjectMeta)

	igKey, err := controller.KeyFunc(cur)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Unable to generate key for %v: %v", cur.Name, err))
		return
	}
	nsKey := nsc.InstanceGroupKeyToNodeSetKey(igKey)
	ns, exists, err := nsc.nodesetIndexer.GetByKey(nsKey)
	if !exists {
		return
	}

	if err != nil {
		// NodeSet is gone
		return
	}

	nsc.enqueueNodeSet(ns)
}

func (nsc *NodeSetController) updateNodeSet(oldObj, curObj interface{}) {
	cur := curObj.(*v1alpha1.NodeSet)

	nsc.enqueueNodeSet(cur)
}

// Update Nodeset status. Returns a new NodeSet
func (nsc *NodeSetController) updateNodeSetStatus(oldns *v1alpha1.NodeSet, status *v1alpha1.NodeSetStatus) (ns *v1alpha1.NodeSet, err error) {
	obj, err := scheme.Scheme.Copy(oldns)
	ns, _ = obj.(*v1alpha1.NodeSet)
	if err != nil {
		return
	}
	ns.Status = *status

	// Patch doesn't work for TPR. We have to do a complete update
	ns, err = nsc.nodesetClient.NodesetV1alpha1().NodeSets().Update(ns)
	if err != nil {
		glog.Errorf("Unable to update NodeSet status %+v: %v", err)
		return
	}

	return
}

func (nsc *NodeSetController) getNodeSetStatusFromInstanceGroup(ig *cluster.InstanceGroup, ns *v1alpha1.NodeSet) *v1alpha1.NodeSetStatus {
	status := &v1alpha1.NodeSetStatus{
		Replicas:           ig.Status.Replicas,
		RunningReplicas:    ig.Status.ReadyReplicas,      // Or should we use available?
		ObservedGeneration: ig.Status.ObservedGeneration, // Don't update generation
	}

	// If IG's ObservedGeneration matches, make NodeSet's ObservedGeneration match
	if ig.Status.ObservedGeneration == ig.Generation {
		status.ObservedGeneration = ns.Generation
	}

	for _, c := range ig.Status.Conditions {
		status.Conditions = append(status.Conditions, v1alpha1.NodeSetCondition{
			Type:               v1alpha1.NodeSetConditionType(c.Type),
			Status:             v1.ConditionStatus(c.Status),
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
		})
	}

	return status
}

// obj could be an *NodeSet or a DeletionFinalStateUnknown marker item.
func (nsc *NodeSetController) enqueueNodeSet(obj interface{}) {
	ns, ok := obj.(*v1alpha1.NodeSet)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %+v", obj))
			return
		}
		ns, ok = tombstone.Obj.(*v1alpha1.NodeSet)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a instance %#v", obj))
			return
		}
	}

	if ns != nil && ns.Spec.NodeSetController != ArchonNodeSetControllerKey {
		// Not out nodeset
		glog.Infof("not our nodeset: %v", ns.Spec.NodeSetController)
		return
	}

	key, err := controller.KeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %+v: %v", obj, err))
		return
	}

	nsc.nodesetQueue.Add(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (nsc *NodeSetController) nodesetWorker() {
	for nsc.processNextWorkItem() {
	}
}

func (nsc *NodeSetController) processNextWorkItem() bool {
	key, quit := nsc.nodesetQueue.Get()
	if quit {
		return false
	}
	defer nsc.nodesetQueue.Done(key)

	err := nsc.syncHandler(key.(string))
	if err == nil {
		nsc.nodesetQueue.Forget(key)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("Sync %q failed with %v", key, err))
	nsc.nodesetQueue.AddRateLimited(key)

	return true
}

// syncNodeSet will sync the NodeSet with the given key to InstanceGroup. Optionally,
// if the corresponding InstanceGroup status is updated, it will sync its status back to NodeSet.
func (nsc *NodeSetController) syncNodeSet(key string) error {
	var (
		ns *v1alpha1.NodeSet
		ig *cluster.InstanceGroup
	)
	obj, nsExists, err := nsc.nodesetIndexer.GetByKey(key)
	if err != nil {
		glog.Errorf("Error listing NodeSet: %v", key)
		return err
	}
	if nsExists && obj != nil {
		ns = obj.(*v1alpha1.NodeSet)
	}

	igKey := nsc.NodeSetKeyToInstanceGroupKey(key)
	obj, igExists, err := nsc.instanceGroupIndexer.GetByKey(igKey)
	if err != nil {
		glog.Errorf("Error listing InstanceGroup of NodeSet %v, igKey: %v", ns.Name, igKey)
		return err
	}
	if igExists && obj != nil {
		ig = obj.(*cluster.InstanceGroup)
	}

	if nsExists == false {
		if igExists == true {
			// NodeSet is deleted. Delete InstanceGroup
			glog.V(4).Infof("NodeSet %v is gone. Removing InstanceGroup %v", key, igKey)
			err = nsc.kubeClient.Archon().InstanceGroups(ig.Namespace).Delete(ig.Name)
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("Unable to delete InstanceGroup: %v", err))
			}
			return err
		}

		// None of them exists.
		return nil
	}

	if nsExists && igExists {
		// Check if IG status needs to be synced back
		newStatus := nsc.getNodeSetStatusFromInstanceGroup(ig, ns)
		if !reflect.DeepEqual(newStatus, &ns.Status) {
			glog.V(4).Infof("Sync InstanceGroup %v status back to NodeSet %v", igKey, key)
			ns, err = nsc.updateNodeSetStatus(ns, newStatus)
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("Unable to update NodeSetStatus: %v", err))
				return err
			}
		}
	}

	// Create or Update
	// Check RV of NodeSet and RVNS in IG annotation
	if igExists && ig != nil && ig.Annotations != nil {
		igrv, _ := ig.Annotations[NodeSetResourceVersionKey]
		if igrv == ns.ResourceVersion {
			// IG is up to date
			return nil
		}
	}

	// There's update. Construct a new IG
	newIG, err := nsc.NodeSetToInstanceGroup(ns)
	if err != nil {
		err = fmt.Errorf("Unable to convert NodeSet to InstanceGroup: %v", err)
		return err
	}

	// TODO: optional, check if data has actually been updated
	if igExists {
		// Do update
		glog.V(4).Infof("Update InstanceGroup %v to RV %s", igKey, ns.ResourceVersion)
		_, err = nsc.kubeClient.Archon().InstanceGroups(newIG.Namespace).Update(newIG)
	} else {
		// Do create
		glog.V(4).Infof("Create InstanceGroup %v of RV %s", igKey, ns.ResourceVersion)
		_, err = nsc.kubeClient.Archon().InstanceGroups(newIG.Namespace).Create(newIG)
	}

	if err != nil {
		glog.Errorf("Unable to create/update InstanceGroup %v: %+v", newIG.Name, err)
	}

	return err
}

// Construct a NodeSet from InstanceGroup
func (nsc *NodeSetController) NodeSetToInstanceGroup(ns *v1alpha1.NodeSet) (ig *cluster.InstanceGroup, err error) {
	nc := (*v1alpha1.NodeClass)(nil)
	if ns.Spec.NodeClass != "" {
		nc, err = nsc.nodesetClient.NodesetV1alpha1().NodeClasses().Get(ns.Spec.NodeClass, metav1.GetOptions{})
		if err != nil {
			err = fmt.Errorf("Unable to get NodeClass %v: %v", ns.Spec.NodeClass, err)
			return
		}
	}

	selector := getDesiredSelectors(ns)

	nodeConfig := &ArchonNodeConfig{}
	err = decodeAndMergeArchonNodeConfig(nc.Config, nodeConfig)
	if err != nil {
		err = fmt.Errorf("Unable to get config from NodeClass %v: %v", nc.Name, err)
		return
	}

	err = decodeAndMergeArchonNodeConfig(ns.Spec.Config, nodeConfig)
	if err != nil {
		err = fmt.Errorf("Unable to get config from NodeSet %v: %v", ns.Name, err)
		return
	}

	users, secrets, files, err := classifyArchonNodeResources(nsc.instanceGroupNamespace, nc.Resources)
	if err != nil {
		err = fmt.Errorf("Unable to classify resources from NodeClass%v: %v", nc.Name, err)
		return
	}

	template := cluster.InstanceTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      selector,
			Annotations: nodeConfig.Annotations,
			Namespace:   nsc.instanceGroupNamespace,
		},
		Spec: cluster.InstanceSpec{
			OS:            nodeConfig.OS,
			Image:         nodeConfig.Image,
			ReclaimPolicy: nodeConfig.ReclaimPolicy,
			InstanceType:  nodeConfig.InstanceType,
			NetworkName:   nodeConfig.NetworkName,
			Hostname:      nodeConfig.Hostname,
			Configs:       nodeConfig.Configs,
			Users:         users,
			Secrets:       secrets,
			Files:         files,
		},
	}

	ig = &cluster.InstanceGroup{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cluster.SchemeGroupVersion.String(),
			Kind:       "InstanceGroup",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        ns.Name,
			Labels:      getDesiredLabels(ns),
			Annotations: getDesiredAnnotations(ns),
			Namespace:   nsc.instanceGroupNamespace,
		},
		Spec: cluster.InstanceGroupSpec{
			Replicas:                 ns.Spec.Replicas,
			Selector:                 &metav1.LabelSelector{MatchLabels: selector},
			ProvisionPolicy:          nodeConfig.ProvisionPolicy,
			MinReadySeconds:          nodeConfig.MinReadySeconds,
			ReservedInstanceSelector: nodeConfig.ReservedInstanceSelector,
			Template:                 template,
		},
	}

	return
}

// Make a NodeSet key from InstanceGroup key
// TODO: When NodeSet moves away from TPR, resulting in a different key, this needs to be modified as well
func (nsc *NodeSetController) InstanceGroupKeyToNodeSetKey(igKey string) string {
	parts := strings.SplitN(igKey, "/", 1)
	if len(parts) == 2 {
		parts[0] = v1alpha1.TPRNamespace
	}

	return strings.Join(parts, "/")
}

// Make a InstanceGroup key from NodeSet key (NodeSet key could be namespace-less)
// TODO: When NodeSet moves away from TPR, resulting in a different key, this needs to be modified as well
func (nsc *NodeSetController) NodeSetKeyToInstanceGroupKey(igKey string) string {
	parts := strings.SplitN(igKey, "/", 2)
	if len(parts) == 2 {
		parts[0] = nsc.instanceGroupNamespace
	} else {
		parts = append([]string{nsc.instanceGroupNamespace}, parts...)
	}

	return strings.Join(parts, "/")
}
