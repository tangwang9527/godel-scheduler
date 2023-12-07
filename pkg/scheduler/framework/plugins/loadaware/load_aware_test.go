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

package loadaware

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framework "github.com/kubewharf/godel-scheduler/pkg/framework/api"
	"github.com/kubewharf/godel-scheduler/pkg/scheduler/apis/config"
	godelcache "github.com/kubewharf/godel-scheduler/pkg/scheduler/cache"
	"github.com/kubewharf/godel-scheduler/pkg/scheduler/cache/handler"
	st "github.com/kubewharf/godel-scheduler/pkg/scheduler/testing"
	testing_helper "github.com/kubewharf/godel-scheduler/pkg/testing-helper"
	podutil "github.com/kubewharf/godel-scheduler/pkg/util/pod"
)

func MakeNode(node string, milliCPU, memory int64) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: node},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(milliCPU, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(memory, resource.BinarySI),
			},
			Allocatable: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(milliCPU, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(memory, resource.BinarySI),
			},
		},
	}
}

func TestLoadAware(t *testing.T) {
	defaultResourceSpec := []config.ResourceSpec{
		{Name: string(v1.ResourceCPU), Weight: 1, ResourceType: podutil.BestEffortPod},
		{Name: string(v1.ResourceMemory), Weight: 1, ResourceType: podutil.BestEffortPod},
	}

	podRequests1 := map[v1.ResourceName]string{v1.ResourceCPU: "1", v1.ResourceMemory: "1Gi"}
	podRequests2 := map[v1.ResourceName]string{v1.ResourceCPU: "2", v1.ResourceMemory: "2Gi"}

	bePod := testing_helper.MakePod().Name("pod").UID("uid").Annotation(podutil.PodResourceTypeAnnotationKey, string(podutil.BestEffortPod)).Req(podRequests1).Obj()
	beEmptyPod := testing_helper.MakePod().Name("pod").UID("uid").Annotation(podutil.PodResourceTypeAnnotationKey, string(podutil.BestEffortPod)).Obj()

	bePod1 := testing_helper.MakePod().Name("pod1").UID("pod1").Annotation(podutil.PodResourceTypeAnnotationKey, string(podutil.BestEffortPod)).Req(podRequests1).Node("machine1").Obj()
	bePod2 := testing_helper.MakePod().Name("pod2").UID("pod2").Annotation(podutil.PodResourceTypeAnnotationKey, string(podutil.BestEffortPod)).Req(podRequests2).Node("machine2").Obj()

	gtPod1 := testing_helper.MakePod().Name("pod1").UID("pod1").Annotation(podutil.PodResourceTypeAnnotationKey, string(podutil.GuaranteedPod)).Req(podRequests1).Node("machine1").Obj()
	gtPod2 := testing_helper.MakePod().Name("pod2").UID("pod2").Annotation(podutil.PodResourceTypeAnnotationKey, string(podutil.GuaranteedPod)).Req(podRequests2).Node("machine2").Obj()

	nodeCapacity := map[v1.ResourceName]string{v1.ResourceCPU: "4", v1.ResourceMemory: "16Gi"}

	nodeInfo1 := testing_helper.MakeNodeInfo().Name("machine1").Capacity(nodeCapacity).CNRCapacity(nodeCapacity)
	nodeInfo2 := testing_helper.MakeNodeInfo().Name("machine2").Capacity(nodeCapacity).CNRCapacity(nodeCapacity)

	testing_helper.MakeNodeInfo()

	tests := []struct {
		pod          *v1.Pod
		pods         []*v1.Pod
		nodeInfos    []framework.NodeInfo
		args         config.LoadAwareArgs
		wantErr      string
		expectedList framework.NodeScoreList
		name         string
	}{
		{
			// Node1 scores on 0-MaxNodeScore scale
			// CPU Score: ((4000 - 250) * MaxNodeScore) / 4000 = 93
			// Memory Score: ((16 - 0.2) * MaxNodeScore) / 10000 = 98
			// Node1 Score: (93 + 98) / 2 = 95
			// Node2 scores on 0-MaxNodeScore scale
			// CPU Score: ((4000 - 250) * MaxNodeScore) / 4000 = 93
			// Memory Score: ((16 - 0.2) * MaxNodeScore) / 10000 = 98
			// Node1 Score: (93 + 98) / 2 = 95
			pod:          beEmptyPod,
			nodeInfos:    []framework.NodeInfo{nodeInfo1.Clone(), nodeInfo2.Clone()},
			args:         config.LoadAwareArgs{Resources: defaultResourceSpec},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: 97}, {Name: "machine2", Score: 97}},
			name:         "nothing scheduled, nothing requested(use least requests)",
		},
		{
			// Node1 scores on 0-MaxNodeScore scale
			// CPU Score: ((4000 - 2000) * MaxNodeScore) / 4000 = 50
			// Memory Score: ((16 - 2) * MaxNodeScore) / 16 = 87
			// Node1 Score: (50 + 87) / 2 = 68
			// Node2 scores on 0-MaxNodeScore scale
			// CPU Score: ((4000 - 3000) * MaxNodeScore) / 4000 = 25
			// Memory Score: ((16 - 3) * MaxNodeScore) / 16 = 81
			// Node2 Score: (25 + 81) / 2 = 53
			pod:          bePod,
			pods:         []*v1.Pod{bePod1, bePod2},
			nodeInfos:    []framework.NodeInfo{nodeInfo1.Clone(), nodeInfo2.Clone()},
			args:         config.LoadAwareArgs{Resources: defaultResourceSpec},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: 68}, {Name: "machine2", Score: 53}},
			name:         "different size pods scheduled, be resources requested, same sized machines",
		},
		{
			// Node1 scores on 0-MaxNodeScore scale
			// CPU Score: ((4000 - 1000) * MaxNodeScore) / 4000 = 75
			// Memory Score: ((16 - 1) * MaxNodeScore) / 16 = 93
			// Node1 Score: (75 + 93) / 2 = 84
			// Node2 scores on 0-MaxNodeScore scale
			// CPU Score: ((4000 - 1000) * MaxNodeScore) / 4000 = 75
			// Memory Score: ((16 - 1) * MaxNodeScore) / 16 = 93
			// Node1 Score: (75 + 93) / 2 = 84
			pod:          bePod,
			pods:         []*v1.Pod{gtPod1, gtPod2},
			nodeInfos:    []framework.NodeInfo{nodeInfo1.Clone(), nodeInfo2.Clone()},
			args:         config.LoadAwareArgs{Resources: defaultResourceSpec},
			expectedList: []framework.NodeScore{{Name: "machine1", Score: 84}, {Name: "machine2", Score: 84}},
			name:         "different gt size pods scheduled, be resources requested, same sized machines, gt pods will be ignored",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schedulerCache := godelcache.New(handler.MakeCacheHandlerWrapper().
				SchedulerName("").SchedulerType("").SubCluster(framework.DefaultSubCluster).
				TTL(time.Second).Period(10 * time.Second).StopCh(make(<-chan struct{})).
				Obj())
			snapshot := godelcache.NewEmptySnapshot(handler.MakeCacheHandlerWrapper().
				SubCluster(framework.DefaultSubCluster).SwitchType(framework.DefaultSubClusterSwitchType).
				Obj())
			{
				// Prepare cache and snapshot.
				for _, p := range test.pods {
					schedulerCache.AddPod(p)
				}
				for _, n := range test.nodeInfos {
					schedulerCache.AddNode(n.GetNode())
					schedulerCache.AddCNR(n.GetCNR())
				}
				schedulerCache.UpdateSnapshot(snapshot)
			}
			fh, _ := st.NewSchedulerFrameworkHandle(nil, nil, nil, nil, schedulerCache, snapshot, nil, nil, nil, nil)

			p, err := NewLoadAware(&test.args, fh)

			if len(test.wantErr) != 0 {
				if err != nil && test.wantErr != err.Error() {
					t.Fatalf("got err %v, want %v", err.Error(), test.wantErr)
				} else if err == nil {
					t.Fatalf("no error produced, wanted %v", test.wantErr)
				}
				return
			}

			if err != nil && len(test.wantErr) == 0 {
				t.Fatalf("failed to initialize plugin NodeResourcesLeastAllocated, got error: %v", err)
			}

			cycleState := framework.NewCycleState()

			resourceType, err := podutil.GetPodResourceType(test.pod)
			assert.NoError(t, err)

			framework.SetPodResourceTypeState(resourceType, cycleState)
			for i := range test.nodeInfos {
				hostResult, err := p.(framework.ScorePlugin).Score(context.Background(), cycleState, test.pod, test.nodeInfos[i].GetNodeName())
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !reflect.DeepEqual(test.expectedList[i].Score, hostResult) {
					t.Errorf("expected %#v, got %#v", test.expectedList[i].Score, hostResult)
				}
			}
		})
	}
}
