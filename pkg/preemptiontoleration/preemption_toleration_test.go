package preemptiontoleration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"

	v1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
)

func TestGetCompletedPreemptionToleration(t *testing.T) {
	tests := []struct {
		name          string
		priorityClass *schedulingv1.PriorityClass
		want          *PreemptionTolerationPolicy
	}{
		{
			name:          "nil just returns nil",
			priorityClass: makePriorityClass(t, samplePriority, nil),
			want:          nil,
		},
		{
			name:          "empty preemption toleration completes MinimumPreemptablePriority",
			priorityClass: makePriorityClass(t, samplePriority, &PreemptionTolerationPolicy{}),
			want: &PreemptionTolerationPolicy{
				MinimumPreemptablePriority: pointer.Int32Ptr(samplePriority + 1),
				TolerationSeconds:          nil,
			},
		},
		{
			name: "preemption toleration does not complete TolerationSeconds",
			priorityClass: makePriorityClass(t, samplePriority, &PreemptionTolerationPolicy{
				MinimumPreemptablePriority: pointer.Int32Ptr(samplePriority + 10),
			}),
			want: &PreemptionTolerationPolicy{
				MinimumPreemptablePriority: pointer.Int32Ptr(samplePriority + 10),
				TolerationSeconds:          nil,
			},
		},
		{
			name: "no empty fields does not complete any fields",
			priorityClass: makePriorityClass(t, samplePriority, &PreemptionTolerationPolicy{
				MinimumPreemptablePriority: pointer.Int32Ptr(samplePriority + 10),
				TolerationSeconds:          pointer.Int64Ptr(10),
			}),
			want: &PreemptionTolerationPolicy{
				MinimumPreemptablePriority: pointer.Int32Ptr(samplePriority + 10),
				TolerationSeconds:          pointer.Int64Ptr(10),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetCompletedPreemptionToleration(*tt.priorityClass)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Unexpected result (-want, +got): %s", diff)
			}
		})
	}
}

var (
	samplePriority    = int32(100)
	priorityClassName = "dummy"
	now               = time.Now()
)

type testCase struct {
	name                         string
	victimCandidatePriorityClass *schedulingv1.PriorityClass
	victimCandidate              *v1.Pod
	preemptor                    *v1.Pod
	wantErr                      bool
	want                         bool
	errSubStr                    string
}

func TestCanToleratePreemptionWithoutTolerationSeconds(t *testing.T) {
	minimumPreemptablePriority := samplePriority + 10
	preemptionToleration := &PreemptionTolerationPolicy{
		// it can tolerate preemption by p < samplePriority
		// (i.e. samplePriority+1 can preempt it)
		MinimumPreemptablePriority: pointer.Int32Ptr(minimumPreemptablePriority),
	}
	for _, tt := range []testCase{
		{
			name:                         "victim candidate can tolerate when preemptor's priority < MinimumPreemptablePriority",
			victimCandidatePriorityClass: makePriorityClass(t, minimumPreemptablePriority-10, preemptionToleration),
			victimCandidate: makeVictimCandidate(
				st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority-10),
				pointer.Int64Ptr(0), // scheduled just now
			),
			preemptor: st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority - 1).Obj(),
			want:      true,
		},
		{
			name:                         "victim candidate can NOT tolerate when preemptor's priority >= MinimumPreemptablePriority even if it is scheduled just now",
			victimCandidatePriorityClass: makePriorityClass(t, minimumPreemptablePriority-10, preemptionToleration),
			victimCandidate: makeVictimCandidate(
				st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority-10),
				pointer.Int64Ptr(0), // scheduled just now
			),
			preemptor: st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority).Obj(),
			want:      false,
		},
		{
			name:                         "unscheduled victim can NOT tolerate preemption when preemptorPriority >= MinimumPreemptablePriority",
			victimCandidatePriorityClass: makePriorityClass(t, minimumPreemptablePriority-10, preemptionToleration),
			victimCandidate:              makeVictimCandidate(st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority-10), nil),
			preemptor:                    st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority).Obj(),
			want:                         false,
		},
		{
			name:                         "victim candidate can NOT tolerate when preemptor's priority >= MinimumPreemptablePriority",
			victimCandidatePriorityClass: makePriorityClass(t, minimumPreemptablePriority-10, preemptionToleration),
			victimCandidate: makeVictimCandidate(
				st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority-10),
				pointer.Int64Ptr(-60), // scheduled 60 seconds ago
			),
			preemptor: st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority).Obj(),
			want:      false,
		},
	} {
		t.Run(tt.name, tt.run)
	}
}

func TestCanToleratePreemptionWithTolerationSeconds(t *testing.T) {
	minimumPreemptablePriority := samplePriority + 10
	tolerationSeconds := int64(600)
	preemptionToleration := &PreemptionTolerationPolicy{
		// it can tolerate preemption by p < samplePriority
		MinimumPreemptablePriority: pointer.Int32Ptr(minimumPreemptablePriority),
		TolerationSeconds:          pointer.Int64Ptr(tolerationSeconds),
	}

	for _, tt := range []testCase{
		{
			name:                         "when preemptor's priority >= MinimumPreemptablePriority, victim candidate just can NOT tolerate the preemption",
			victimCandidatePriorityClass: makePriorityClass(t, minimumPreemptablePriority-10, preemptionToleration),
			victimCandidate:              makeVictimCandidate(st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority-10), pointer.Int64Ptr(100)),
			preemptor:                    st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority).Obj(),
			want:                         false,
		},
		{
			name:                         "when preemptor's priority < MinimumPreemptablePriority, victim that has not yet elapsed TolerationSeconds since being scheduled can tolerate the preemption",
			victimCandidatePriorityClass: makePriorityClass(t, minimumPreemptablePriority-10, preemptionToleration),
			victimCandidate: makeVictimCandidate(
				st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority-10),
				pointer.Int64Ptr(-1*(tolerationSeconds-1)), // scheduled tolerationSeconds-1 ago
			),
			preemptor: st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority - 1).Obj(),
			want:      true,
		},
		{
			name:                         "when preemptor's priority < MinimumPreemptablePriority, victim that has elapsed TolerationSeconds since being scheduled can NOT tolerate the preemption",
			victimCandidatePriorityClass: makePriorityClass(t, minimumPreemptablePriority-10, preemptionToleration),
			victimCandidate: makeVictimCandidate(
				st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority-10),
				pointer.Int64Ptr(-2*tolerationSeconds), // scheduled 2*tolerationSeconds ago
			),
			preemptor: st.MakePod().Name("p").UID("p").Priority(minimumPreemptablePriority - 1).Obj(),
			want:      false,
		},
	} {
		t.Run(tt.name, tt.run)
	}
}

func makeVictimCandidate(pw *st.PodWrapper, secondsFromScheduled *int64) *v1.Pod {
	pw.Spec.PriorityClassName = priorityClassName
	if secondsFromScheduled != nil {
		pw.Status.Conditions = []v1.PodCondition{{
			Type:               v1.PodScheduled,
			LastTransitionTime: metav1.Time{Time: now.Add(time.Duration(*secondsFromScheduled) * time.Second)},
		}}
	}
	return pw.Obj()
}

func makePriorityClass(t *testing.T, value int32, policy *PreemptionTolerationPolicy) *schedulingv1.PriorityClass {
	pc := &schedulingv1.PriorityClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: schedulingv1.SchemeGroupVersion.String(),
			Kind:       "PriorityClass",
		},
		ObjectMeta: metav1.ObjectMeta{Name: priorityClassName, Annotations: map[string]string{}},
		Value:      value,
	}
	if policy == nil {
		return pc
	}
	pc.Annotations[AnnotationKeyPrefix+"enabled"] = ""
	if policy.MinimumPreemptablePriority != nil {
		pc.Annotations[AnnotationKeyPrefix+"minimum-preemptable-priority"] = fmt.Sprintf("%d", *policy.MinimumPreemptablePriority)
	}
	if policy.TolerationSeconds != nil {
		pc.Annotations[AnnotationKeyPrefix+"toleration-seconds"] = fmt.Sprintf("%d", *policy.TolerationSeconds)
	}
	return pc
}

func (tt testCase) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakeClient := fake.NewSimpleClientset(tt.victimCandidatePriorityClass)
	informersFactory := informers.NewSharedInformerFactory(fakeClient, 1*time.Minute)
	pcInformer := informersFactory.Scheduling().V1().PriorityClasses().Informer()
	pcInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{})
	informersFactory.Start(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), pcInformer.HasSynced)

	got, err := CanToleratePreemption(
		tt.victimCandidate, tt.preemptor,
		informersFactory.Scheduling().V1().PriorityClasses().Lister(),
		now,
	)

	if tt.wantErr {
		if err == nil {
			t.Errorf("expected error")
			return
		}
		if !strings.Contains(err.Error(), tt.errSubStr) {
			t.Errorf("Unexpected error message wantSubString: %s, got: %s", tt.errSubStr, err.Error())
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		if tt.want != got {
			t.Errorf("Unexpected result want: %v, got: %v", tt.want, got)
		}
	}
}
