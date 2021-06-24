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

package integration

import (
	"testing"
	"time"

	"github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/pkg/scheduler"
	schedapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	fwkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	testutils "k8s.io/kubernetes/test/integration/util"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/scheduler-plugins/pkg/apis/config/v1beta1"
	"sigs.k8s.io/scheduler-plugins/pkg/preemptiontoleration"
	"sigs.k8s.io/scheduler-plugins/test/util"
	"sigs.k8s.io/yaml"
)

var (
	testPriorityClassName = "priority-class-for-victim-candidates"
	testPriority          = int32(1000)
	pause                 = imageutils.GetPauseImageName()
	// nodeCapacity < 2 * podRequest
	nodeCapacity = map[v1.ResourceName]string{
		v1.ResourceCPU:    "3",
		v1.ResourceMemory: "3Gi",
	}
	podRequest = v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse("2"),
		v1.ResourceMemory: resource.MustParse("1Gi"),
	}
)

func TestPreemptionTolerationPlugin(t *testing.T) {
	node := st.MakeNode().Name("node-a").Capacity(nodeCapacity).Obj()
	victimCandidate := mkPod(
		st.MakePod().Name("victim-candidate").Priority(testPriority).Container(pause).ZeroTerminationGracePeriod(),
		withPriorityClass(testPriorityClassName),
		withResource(podRequest),
	).Obj()
	tests := []struct {
		name                  string
		priorityClass         *schedulingv1.PriorityClass
		preemptor             *v1.Pod
		canTolerate           bool
		waitPreemptorCreation time.Duration
	}{
		{
			name: "can tolerate the preemption because preemptor's priority < MinimumPreemptablePriority(=testPriority+10)",
			priorityClass: makeTestPriorityClass(t, &v1beta1.PreemptionToleration{
				MinimumPreemptablePriority: pointer.Int32Ptr(testPriority + 10),
			}),
			preemptor:   mkPreemptor(testPriority + 9),
			canTolerate: true,
		},
		{
			name: "can NOT tolerate the preemption because preemptor's priority >= MinimumPreemptablePriority(=testPriority+10)",
			priorityClass: makeTestPriorityClass(t, &v1beta1.PreemptionToleration{
				MinimumPreemptablePriority: pointer.Int32Ptr(testPriority + 10),
			}),
			preemptor:   mkPreemptor(testPriority + 10),
			canTolerate: false,
		},
		{
			name: "when preemptor's priority >= MinimumPreemptablePriority(=testPriority+10), TolerationSeconds has no effect (it can NOT tolerate the preemption)",
			priorityClass: makeTestPriorityClass(t, &v1beta1.PreemptionToleration{
				MinimumPreemptablePriority: pointer.Int32Ptr(testPriority + 10),
				TolerationSeconds:          pointer.Int64Ptr(30),
			}),
			preemptor:   mkPreemptor(testPriority + 10),
			canTolerate: false,
		},
		{
			name: "when preemptor's priority < MinimumPreemptablePriority(=testPriority+10), it can tolerate the preemption in TolerationSeconds",
			priorityClass: makeTestPriorityClass(t, &v1beta1.PreemptionToleration{
				MinimumPreemptablePriority: pointer.Int32Ptr(testPriority + 10),
				TolerationSeconds:          pointer.Int64Ptr(30),
			}),
			preemptor:   mkPreemptor(testPriority + 5),
			canTolerate: true,
		},
		{
			name: "when preemptor's priority < MinimumPreemptablePriority(=testPriority+10), it can NOT tolerate the preemption after TolerationSeconds elapsed",
			priorityClass: makeTestPriorityClass(t, &v1beta1.PreemptionToleration{
				MinimumPreemptablePriority: pointer.Int32Ptr(testPriority + 10),
				TolerationSeconds:          pointer.Int64Ptr(5),
			}),
			preemptor:             mkPreemptor(testPriority + 5),
			waitPreemptorCreation: 10 * time.Second,
			canTolerate:           false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// prepare test cluster
			registry := fwkruntime.Registry{preemptiontoleration.Name: preemptiontoleration.New}
			profile := schedapi.KubeSchedulerProfile{
				SchedulerName: v1.DefaultSchedulerName,
				Plugins: &schedapi.Plugins{
					PostFilter: &schedapi.PluginSet{
						Enabled: []schedapi.Plugin{
							{Name: preemptiontoleration.Name},
						},
						Disabled: []schedapi.Plugin{
							{Name: "*"},
						},
					},
				},
			}
			testCtx := util.InitTestSchedulerWithOptions(
				t,
				testutils.InitTestMaster(t, "sched-preemptiontoleration", nil),
				true,
				scheduler.WithProfiles(profile),
				scheduler.WithFrameworkOutOfTreeRegistry(registry),
				scheduler.WithPodInitialBackoffSeconds(int64(0)),
				scheduler.WithPodMaxBackoffSeconds(int64(0)),
			)
			defer testutils.CleanupTest(t, testCtx)

			cs, ns := testCtx.ClientSet, testCtx.NS.Name

			// Create test node
			if _, err := cs.CoreV1().Nodes().Create(testCtx.Ctx, node, metav1.CreateOptions{}); err != nil {
				t.Fatalf("failed to create node: %v", err)
			}

			// create test priorityclass for evaluating PreemptionToleration policy
			if _, err := cs.SchedulingV1().PriorityClasses().Create(testCtx.Ctx, tt.priorityClass, metav1.CreateOptions{}); err != nil {
				t.Fatalf("failed to create priorityclass %s: %v", tt.priorityClass.Name, err)
			}

			// Create victim candidate pod and it should be scheduled
			if _, err := cs.CoreV1().Pods(ns).Create(testCtx.Ctx, victimCandidate, metav1.CreateOptions{}); err != nil {
				t.Fatalf("failed to create victim candidate pod %q: %v", victimCandidate.Name, err)
			}
			if err := wait.Poll(1*time.Second, 60*time.Second, func() (bool, error) {
				return podScheduled(cs, ns, victimCandidate.Name), nil
			}); err != nil {
				t.Fatalf("victim candidate pod %q failed to be scheduled: %v", victimCandidate.Name, err)
			}
			// simulate pod scheduled time
			if err := wait.Poll(1*time.Second, 60*time.Second, func() (bool, error) {
				vc, err := cs.CoreV1().Pods(ns).Get(testCtx.Ctx, victimCandidate.Name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				vc.Status.Conditions = []v1.PodCondition{{
					Type:               v1.PodScheduled,
					LastTransitionTime: metav1.Time{Time: time.Now()},
				}}
				_, err = cs.CoreV1().Pods(ns).UpdateStatus(testCtx.Ctx, vc, metav1.UpdateOptions{})
				if err != nil {
					return false, err
				}
				return true, nil
			}); err != nil {
				t.Fatalf("failed to update victim candidate pod %q scheduled time: %v", victimCandidate.Name, err)
			}

			// Create the preemptor pod.
			<-time.After(tt.waitPreemptorCreation)
			if _, err := cs.CoreV1().Pods(ns).Create(testCtx.Ctx, tt.preemptor, metav1.CreateOptions{}); err != nil {
				t.Fatalf("failed to create preemptor Pod %q: %v", tt.preemptor.Name, err)
			}

			if tt.canTolerate {
				// - the victim candidate pod keeps being scheduled, and
				// - the preemptor pod is not scheduled
				if err := consistently(1*time.Second, 15*time.Second, func() (bool, error) {
					a := podScheduled(cs, ns, victimCandidate.Name)
					b := podScheduled(cs, ns, tt.preemptor.Name)
					t.Logf("%s scheduled=%v, %s scheduled = %v", victimCandidate.Name, a, tt.preemptor.Name, b)
					return a && !b, nil
				}); err != nil {
					t.Fatalf("preemptor pod %q was scheduled: %v", tt.preemptor.Name, err)
				}
			} else {
				// - the preemtor pod got scheduled successfully
				// - the victim pod does not exist (preempted)
				if err := wait.Poll(1*time.Second, 30*time.Second, func() (bool, error) {
					return podScheduled(cs, ns, tt.preemptor.Name) && podNotExist(cs, ns, victimCandidate.Name), nil
				}); err != nil {
					t.Fatalf("preemptor pod %q failed to be scheduled: %v", tt.preemptor.Name, err)
				}
			}
		})
	}
}

func consistently(interval, duration time.Duration, condition func() (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	finished := time.After(duration)
	count := 1
	for {
		select {
		case <-ticker.C:
			ok, err := condition()
			if err != nil {
				return err
			}
			if !ok {
				return errors.Errorf("the condition has not satisfied in the duration at %d th try", count)
			}
			count += 1
		case <-finished:
			return nil
		}
	}
}

func makeTestPriorityClass(t *testing.T, pt *v1beta1.PreemptionToleration) *schedulingv1.PriorityClass {
	pc := &schedulingv1.PriorityClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: schedulingv1.SchemeGroupVersion.String(),
			Kind:       "PriorityClass",
		},
		ObjectMeta: metav1.ObjectMeta{Name: testPriorityClassName, Annotations: map[string]string{}},
		Value:      testPriority,
	}
	if pt != nil {
		pt.APIVersion = v1beta1.SchemeGroupVersion.String()
		pt.Kind = "PreemptionToleration"
		raw, err := yaml.Marshal(pt)
		if err != nil {
			t.Fatal(err)
		}
		pc.Annotations[preemptiontoleration.AnnotationKey] = string(raw)
	}
	return pc
}

func mkPreemptor(p int32) *v1.Pod {
	return mkPod(
		st.MakePod().Name("p").Priority(p).Container(pause).ZeroTerminationGracePeriod(),
		withResource(podRequest),
	).Obj()
}

type mkPodOption = func(pw *st.PodWrapper)

func mkPod(pw *st.PodWrapper, opts ...mkPodOption) *st.PodWrapper {
	for _, opt := range opts {
		opt(pw)
	}
	return pw
}

func withResource(resource v1.ResourceList) mkPodOption {
	return func(pw *st.PodWrapper) {
		for i := range pw.Spec.Containers {
			c := pw.Spec.Containers[i]
			c.Resources.Requests = resource
			pw.Spec.Containers[i] = c
		}
	}
}

func withPriorityClass(priorityClassName string) mkPodOption {
	return func(pw *st.PodWrapper) {
		pw.Spec.PriorityClassName = priorityClassName
	}
}
