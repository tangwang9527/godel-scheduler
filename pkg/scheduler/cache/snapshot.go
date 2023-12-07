/*
Copyright 2023 The Godel Scheduler Authors.

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

package cache

import (
	"fmt"

	schedulingv1a1 "github.com/kubewharf/godel-scheduler-api/pkg/apis/scheduling/v1alpha1"

	framework "github.com/kubewharf/godel-scheduler/pkg/framework/api"
	"github.com/kubewharf/godel-scheduler/pkg/scheduler/cache/commonstores"
	nodestore "github.com/kubewharf/godel-scheduler/pkg/scheduler/cache/commonstores/node_store"
	pdbstore "github.com/kubewharf/godel-scheduler/pkg/scheduler/cache/commonstores/pdb_store"
	podgroupstore "github.com/kubewharf/godel-scheduler/pkg/scheduler/cache/commonstores/podgroup_store"
	preemptionstore "github.com/kubewharf/godel-scheduler/pkg/scheduler/cache/commonstores/preemption_store"
	"github.com/kubewharf/godel-scheduler/pkg/scheduler/cache/handler"
)

// Snapshot is a snapshot of s NodeInfo and NodeTree order. The scheduler takes a
// snapshot at the beginning of each scheduling cycle and uses it for its operations in that cycle.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
type Snapshot struct {
	handler handler.CacheHandler

	nodeSlices *nodeSlices

	storeSwitch *CommonStoresSwitch
}

var _ framework.SharedLister = &Snapshot{}

// NewEmptySnapshot initializes a Snapshot struct and returns it.
func NewEmptySnapshot(handler handler.CacheHandler) *Snapshot {
	nodeSlices := newNodeSlices()

	s := &Snapshot{
		handler: handler,

		nodeSlices: nodeSlices,

		storeSwitch: makeStoreSwitch(handler, commonstores.Snapshot),
	}
	nodeStore := s.storeSwitch.Find(nodestore.Name)
	nodeStore.(*nodestore.NodeStore).AfterAdd = func(n framework.NodeInfo) { nodeSlices.update(n, true) }
	nodeStore.(*nodestore.NodeStore).AfterDelete = func(n framework.NodeInfo) { nodeSlices.update(n, false) }

	handler.SetNodeHandler(nodeStore.(*nodestore.NodeStore).GetNodeInfo)

	return s
}

func (s *Snapshot) MakeBasicNodeGroup() framework.NodeGroup {
	nodeGroup := framework.NewNodeGroup(
		framework.DefaultNodeGroupName,
		[]framework.NodeCircle{framework.NewNodeCircle(framework.DefaultNodeCircleName, s)})
	nodeGroup.SetPreferredNodes(framework.NewPreferredNodes())
	return nodeGroup
}

// GetNodeInfo returns a NodeInfo according to the nodeName.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) GetNodeInfo(nodeName string) framework.NodeInfo {
	return s.storeSwitch.Find(nodestore.Name).(*nodestore.NodeStore).Get(nodeName)
}

// NodeInfos returns a NodeInfoLister.
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) NodeInfos() framework.NodeInfoLister {
	return s
}

// NumNodes returns the number of nodes in the snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) NumNodes() int {
	return s.nodeSlices.inPartitionNodeSlice.Len() + s.nodeSlices.outOfPartitionNodeSlice.Len()
}

// List returns the list of nodes in the snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) List() []framework.NodeInfo {
	return append(s.nodeSlices.inPartitionNodeSlice.Nodes(), s.nodeSlices.outOfPartitionNodeSlice.Nodes()...)
}

// InPartitionList returns the list of nodes which are in the partition of the scheduler
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) InPartitionList() []framework.NodeInfo {
	return s.nodeSlices.inPartitionNodeSlice.Nodes()
}

// OutOfPartitionList returns the list of nodes which are out of the partition of the scheduler
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) OutOfPartitionList() []framework.NodeInfo {
	return s.nodeSlices.outOfPartitionNodeSlice.Nodes()
}

// HavePodsWithAffinityList returns the list of nodes with at least one pod with inter-pod affinity
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) HavePodsWithAffinityList() []framework.NodeInfo {
	return s.nodeSlices.havePodsWithAffinityNodeSlice.Nodes()
}

// HavePodsWithRequiredAntiAffinityList returns the list of nodes with at least one pod with
// required inter-pod anti-affinity
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) HavePodsWithRequiredAntiAffinityList() []framework.NodeInfo {
	return s.nodeSlices.havePodsWithRequiredAntiAffinityNodeSlice.Nodes()
}

// Get returns the NodeInfo of the given node name.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) Get(nodeName string) (framework.NodeInfo, error) {
	if nodeInfo := s.storeSwitch.Find(nodestore.Name).(*nodestore.NodeStore).Get(nodeName); nodeInfo != nil {
		if nodeInfo.GetNode() != nil || nodeInfo.GetNMNode() != nil {
			return nodeInfo, nil
		}
	}
	return nil, fmt.Errorf("nodeinfo not found for node name %q", nodeName)
}

// AssumePod add pod and remove victims in snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) AssumePod(podInfo *framework.CachePodInfo) error {
	return s.storeSwitch.Range(func(cs commonstores.CommonStore) error { return cs.AssumePod(podInfo) })
}

// ForgetPod remove pod and add-back victims in snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) ForgetPod(podInfo *framework.CachePodInfo) error {
	return s.storeSwitch.Range(func(cs commonstores.CommonStore) error { return cs.ForgetPod(podInfo) })
}

// GetPreemptorsByVictim return preemptors by victim.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) GetPreemptorsByVictim(node, victim string) []string {
	// TODO: Remove GetPreemptorsByVictim interface and expose Store by ScheduleFrameworkHandler directly.
	return s.storeSwitch.Find(preemptionstore.Name).(*preemptionstore.PreemptionStore).GetPreemptorsByVictim(node, victim)
}

// GetPDBItemList return PDB items in snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) GetPDBItemList() []framework.PDBItem {
	// TODO: Remove GetPDBItemList interface and expose Store by ScheduleFrameworkHandler directly.
	return s.storeSwitch.Find(pdbstore.Name).(*pdbstore.PdbStore).GetPDBItemList()
}

func (s *Snapshot) GetPodGroupInfo(podGroupName string) (*schedulingv1a1.PodGroup, error) {
	// TODO: Remove GetPodGroupInfo interface and expose Store by ScheduleFrameworkHandler directly.
	return s.storeSwitch.Find(podgroupstore.Name).(*podgroupstore.PodGroupStore).GetPodGroupInfo(podGroupName)
}

// GetPDBItemListForOwner return PDB items for the owner in snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) GetPDBItemListForOwner(ownerType, ownerKey string) (bool, bool, []string) {
	// TODO: Remove GetPDBItemListForOwner interface and expose Store by ScheduleFrameworkHandler directly.
	return s.storeSwitch.Find(pdbstore.Name).(*pdbstore.PdbStore).GetPDBsForOwner(ownerType, ownerKey)
}

// GetOwnerLabels return owner labels in snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) GetOwnerLabels(ownerType, ownerKey string) map[string]string {
	// TODO: Remove GetOwnerLabels interface and expose Store by ScheduleFrameworkHandler directly.
	return s.storeSwitch.Find(pdbstore.Name).(*pdbstore.PdbStore).GetOwnerLabels(ownerType, ownerKey)
}

// GetOwnerLabels return owner labels in snapshot.
//
// Note: Snapshot operations are lock-free. Our premise for removing lock: even if read operations
// are concurrent, write operations(AssumePod/ForgetPod/AddOneVictim) should always be serial.
func (s *Snapshot) GetOwnersForPDB(key, ownerType string) []string {
	// TODO: Remove GetOwnersForPDB interface and expose Store by ScheduleFrameworkHandler directly.
	return s.storeSwitch.Find(pdbstore.Name).(*pdbstore.PdbStore).GetOwnersForPDB(key, ownerType)
}

// -------------------------------------- node slice for snapshot --------------------------------------

type nodeSlices struct {
	inPartitionNodeSlice                      framework.NodeHashSlice
	outOfPartitionNodeSlice                   framework.NodeHashSlice
	havePodsWithAffinityNodeSlice             framework.NodeHashSlice
	havePodsWithRequiredAntiAffinityNodeSlice framework.NodeHashSlice
}

func newNodeSlices() *nodeSlices {
	return &nodeSlices{
		inPartitionNodeSlice:                      framework.NewNodeHashSlice(),
		outOfPartitionNodeSlice:                   framework.NewNodeHashSlice(),
		havePodsWithAffinityNodeSlice:             framework.NewNodeHashSlice(),
		havePodsWithRequiredAntiAffinityNodeSlice: framework.NewNodeHashSlice(),
	}
}

func op(slice framework.NodeHashSlice, n framework.NodeInfo, isAdd bool) {
	if isAdd {
		_ = slice.Add(n)
	} else {
		_ = slice.Del(n)
	}
}

func (s *nodeSlices) update(n framework.NodeInfo, isAdd bool) {
	// ATTENTION: We should ensure that the `globalNodeInfoPlaceHolder` will not be added to nodelice.
	if n == nodestore.GlobalNodeInfoPlaceHolder {
		return
	}

	if n.GetNodeInSchedulerPartition() || n.GetNMNodeInSchedulerPartition() {
		op(s.inPartitionNodeSlice, n, isAdd)
	} else {
		op(s.outOfPartitionNodeSlice, n, isAdd)
	}
	if len(n.GetPodsWithAffinity()) > 0 {
		op(s.havePodsWithAffinityNodeSlice, n, isAdd)
	}
	if len(n.GetPodsWithRequiredAntiAffinity()) > 0 {
		op(s.havePodsWithRequiredAntiAffinityNodeSlice, n, isAdd)
	}
}
