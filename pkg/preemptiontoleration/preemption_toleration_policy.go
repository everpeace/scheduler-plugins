package preemptiontoleration

import (
	"strconv"

	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/utils/pointer"
)

// PreemptionTolerationPolicy holds preemption toleration policy configuration.  Each property values are annotated in the target PriorityClass resource.
type PreemptionTolerationPolicy struct {
	// MinimumPreemptablePriority specifies the minimum priority value that can preempt this priority class.
	MinimumPreemptablePriority *int32

	// TolerationSeconds specifies how long this priority class can tolerate preemption by priorities lower than MinimumPreemptablePriority.
	// Nil value means forever, which is not preempted forever (default)
	// zero and negative value means immediate, which is preempted immediately. i.e. no toleration at all.
	// This value affects scheduled pods only (no effect on nominated pods).
	TolerationSeconds *int64
}

func GetCompletedPreemptionToleration(
	pc schedulingv1.PriorityClass,
) (*PreemptionTolerationPolicy, error) {
	if _, ok := pc.Annotations[AnnotationKeyPrefix+"enabled"]; !ok {
		return nil, nil
	}

	policy := &PreemptionTolerationPolicy{}

	minimumPreemptablePriorityStr, ok := pc.Annotations[AnnotationKeyPrefix+"minimum-preemptable-priority"]
	if !ok {
		policy.MinimumPreemptablePriority = pointer.Int32Ptr(pc.Value + 1)
	} else {
		minimumPreemptablePriority, err := strconv.ParseInt(minimumPreemptablePriorityStr, 10, 32)
		if err != nil {
			return nil, err
		}
		policy.MinimumPreemptablePriority = pointer.Int32Ptr(int32(minimumPreemptablePriority))
	}

	tolerationSecondsStr, ok := pc.Annotations[AnnotationKeyPrefix+"toleration-seconds"]
	if ok {
		tolerationSeconds, err := strconv.ParseInt(tolerationSecondsStr, 10, 64)
		if err != nil {
			return nil, err
		}
		policy.TolerationSeconds = &tolerationSeconds
	}

	return policy, nil
}
