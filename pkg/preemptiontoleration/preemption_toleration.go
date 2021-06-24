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

package preemptiontoleration

import (
	"context"
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	policy "k8s.io/api/policy/v1beta1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corelisters "k8s.io/client-go/listers/core/v1"
	policylisters "k8s.io/client-go/listers/policy/v1beta1"
	schedulinglisters "k8s.io/client-go/listers/scheduling/v1"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/klog/v2"
	extenderv1 "k8s.io/kube-scheduler/extender/v1"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/validation"
	"k8s.io/kubernetes/pkg/scheduler/core"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	dp "k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultpreemption"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	"k8s.io/kubernetes/pkg/scheduler/util"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/scheduler-plugins/pkg/apis/config"
	configscheme "sigs.k8s.io/scheduler-plugins/pkg/apis/config/scheme"
	"sigs.k8s.io/scheduler-plugins/pkg/apis/config/v1beta1"

	schedulerapisconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
)

const (
	// Name of the plugin used in the plugin registry and configurations.
	Name          = "PreemptionToleration"
	AnnotationKey = "scheduling.sigs.k8s.io/preemption-toleration"
)

var (
	_             framework.PostFilterPlugin = &PreemptionToleration{}
	configDecoder runtime.Decoder            = configscheme.Codecs.UniversalDecoder()
	pluginArgsGVK schema.GroupVersionKind    = v1beta1.SchemeGroupVersion.WithKind("PreemptionTolerationPluginArgs")
)

// PreemptionToleration is a PostFilter plugin implements the preemption logic.
type PreemptionToleration struct {
	fh                  framework.Handle
	args                config.PreemptionTolerationArgs
	podLister           corelisters.PodLister
	pdbLister           policylisters.PodDisruptionBudgetLister
	priorityClassLister schedulinglisters.PriorityClassLister
	clock               util.Clock
}

var _ framework.PostFilterPlugin = &PreemptionToleration{}

// Name returns name of the plugin. It is used in logs, etc.
func (pl *PreemptionToleration) Name() string {
	return Name
}

// New initializes a new plugin and returns it.
func New(rawArgs runtime.Object, fh framework.Handle) (framework.Plugin, error) {
	args, ok := rawArgs.(*config.PreemptionTolerationArgs)
	if !ok {
		return nil, fmt.Errorf("got args of type %T, want *PreemptionTolerationArgs", args)
	}

	// reuse validation logic for DefaultPreemptionArgs
	if err := validation.ValidateDefaultPreemptionArgs(schedulerapisconfig.DefaultPreemptionArgs{
		MinCandidateNodesAbsolute:   args.MinCandidateNodesAbsolute,
		MinCandidateNodesPercentage: args.MinCandidateNodesPercentage,
	}); err != nil {
		return nil, err
	}

	pl := PreemptionToleration{
		fh:                  fh,
		args:                *args,
		podLister:           fh.SharedInformerFactory().Core().V1().Pods().Lister(),
		priorityClassLister: fh.SharedInformerFactory().Scheduling().V1().PriorityClasses().Lister(),
		pdbLister:           getPDBLister(fh.SharedInformerFactory()),
		clock:               util.RealClock{},
	}
	return &pl, nil
}

// PostFilter invoked at the postFilter extension point.
func (pl *PreemptionToleration) PostFilter(ctx context.Context, state *framework.CycleState, pod *v1.Pod, m framework.NodeToStatusMap) (*framework.PostFilterResult, *framework.Status) {
	preemptionStartTime := time.Now()
	defer func() {
		metrics.PreemptionAttempts.Inc()
		metrics.DeprecatedSchedulingAlgorithmPreemptionEvaluationDuration.Observe(metrics.SinceInSeconds(preemptionStartTime))
	}()

	nnn, err := pl.preempt(ctx, state, pod, m)
	if err != nil {
		return nil, framework.AsStatus(err)
	}
	if nnn == "" {
		return nil, framework.NewStatus(framework.Unschedulable)
	}
	return &framework.PostFilterResult{NominatedNodeName: nnn}, framework.NewStatus(framework.Success)
}

// preempt finds nodes with pods that can be preempted to make room for "preemptor" to schedule.
// This is almost identical to DefaultPreemption plugin's one.
func (pl *PreemptionToleration) preempt(ctx context.Context, state *framework.CycleState, preemptor *v1.Pod, m framework.NodeToStatusMap) (string, error) {
	cs := pl.fh.ClientSet()
	ph := pl.fh.PreemptHandle()
	nodeLister := pl.fh.SnapshotSharedLister().NodeInfos()

	preemptor, err := pl.podLister.Pods(preemptor.Namespace).Get(preemptor.Name)
	if err != nil {
		klog.Errorf("Error getting the updated preemptor pod object: %v", err)
		return "", err
	}

	// 1) Ensure the preemptor is eligible to preempt other pods.
	if !dp.PodEligibleToPreemptOthers(preemptor, nodeLister, m[preemptor.Status.NominatedNodeName]) {
		klog.V(5).Infof("Pod %v/%v is not eligible for more preemption.", preemptor.Namespace, preemptor.Name)
		return "", nil
	}

	// 2) Find all preemption candidates.
	candidates, err := pl.FindCandidates(ctx, state, preemptor, m)
	if err != nil || len(candidates) == 0 {
		return "", err
	}

	// 3) Interact with registered Extenders to filter out some candidates if needed.
	candidates, err = dp.CallExtenders(ph.Extenders(), preemptor, nodeLister, candidates)
	if err != nil {
		return "", err
	}

	// 4) Find the best candidate.
	bestCandidate := dp.SelectCandidate(candidates)
	if bestCandidate == nil || len(bestCandidate.Name()) == 0 {
		return "", nil
	}

	// 5) Perform preparation work before nominating the selected candidate.
	if err := dp.PrepareCandidate(bestCandidate, pl.fh, cs, preemptor); err != nil {
		return "", err
	}

	return bestCandidate.Name(), nil
}

// CanToleratePreemption evaluates whether the victimCandidate
// pod can tolerate from preemption by the preemptor pod or not
// by inspecting PriorityClass of victimCandidate pod.
func CanToleratePreemption(
	victimCandidate, preemptor *v1.Pod,
	pcLister schedulinglisters.PriorityClassLister,
	now time.Time,
) (bool, error) {
	preemptorPriority := corev1helpers.PodPriority(preemptor)
	victimPriority := corev1helpers.PodPriority(victimCandidate)

	if victimCandidate.Spec.PriorityClassName == "" {
		return false, nil
	}
	victimPriorityClass, err := pcLister.Get(victimCandidate.Spec.PriorityClassName)
	if err != nil {
		return false, err
	}

	policy, err := GetCompletedPreemptionToleration(*victimPriorityClass)
	if err != nil {
		return false, nil
	}
	if policy == nil {
		// if no preemption toleration policy is not defined
		// return whether preemptor is eligible to preemption or not
		// according preemptor.Spec.PreemptionPolicy
		preemptionPolicy := v1.PreemptLowerPriority
		if preemptor.Spec.PreemptionPolicy != nil {
			preemptionPolicy = *preemptor.Spec.PreemptionPolicy
		}
		if preemptionPolicy == v1.PreemptNever {
			return true, nil
		}
		if preemptionPolicy == v1.PreemptLowerPriority {
			return !(preemptorPriority > victimPriority), nil
		}
		return false, nil
	}

	// check it can tolerate the preemption in terms of priority value
	canTolerateOnPriorityValue := preemptorPriority < *policy.MinimumPreemptablePriority
	if !canTolerateOnPriorityValue {
		return canTolerateOnPriorityValue, nil
	}

	if policy.TolerationSeconds == nil {
		return canTolerateOnPriorityValue, nil
	}

	// check it can tolerate the preemption in terms of toleration seconds
	_, scheduledCondition := podutil.GetPodCondition(&victimCandidate.Status, v1.PodScheduled)
	if scheduledCondition == nil {
		return canTolerateOnPriorityValue, nil
	}
	canTolerateOnTolerationSeconds := (policy.TolerationSeconds != nil) &&
		!now.After(scheduledCondition.LastTransitionTime.Time.Add(time.Duration(*policy.TolerationSeconds)*time.Second))

	return canTolerateOnPriorityValue && canTolerateOnTolerationSeconds, nil
}

func getCompletedPreemptionToleration(
	pc schedulingv1.PriorityClass,
) (*config.PreemptionToleration, error) {
	tolerationConfigRaw, ok := pc.Annotations[AnnotationKey]
	if !ok {
		return nil, nil
	}

	pt := &config.PreemptionToleration{}
	_, _, err := configDecoder.Decode(([]byte)(tolerationConfigRaw), nil, pt)
	if err != nil {
		return nil, err
	}

	if pt.MinimumPreemptablePriority == nil {
		pt.MinimumPreemptablePriority = pointer.Int32Ptr(pc.Value + 1)
	}

	return pt, nil
}

// FindCandidates calculates a slice of preemption candidates.
// Each candidate is executable to make the given <preemptor> pod schedulable.
// This is almost identical to DefaultPreemption plugin's one.
func (pl *PreemptionToleration) FindCandidates(
	ctx context.Context,
	state *framework.CycleState,
	preemptor *v1.Pod,
	m framework.NodeToStatusMap,
) ([]dp.Candidate, error) {
	allNodes, err := pl.fh.SnapshotSharedLister().NodeInfos().List()
	if err != nil {
		return nil, err
	}
	if len(allNodes) == 0 {
		return nil, core.ErrNoNodesAvailable
	}

	potentialNodes := nodesWherePreemptionMightHelp(allNodes, m)
	if len(potentialNodes) == 0 {
		klog.V(3).Infof("Preemption will not help schedule pod %v/%v on any node.", preemptor.Namespace, preemptor.Name)
		// In this case, we should clean-up any existing nominated node name of the pod.
		if err := util.ClearNominatedNodeName(pl.fh.ClientSet(), preemptor); err != nil {
			klog.Errorf("Cannot clear 'NominatedNodeName' field of pod %v/%v: %v", preemptor.Namespace, preemptor.Name, err)
			// We do not return as this error is not critical.
		}
		return nil, nil
	}

	pdbs, err := getPodDisruptionBudgets(pl.pdbLister)
	if err != nil {
		return nil, err
	}

	offset, numCandidates := pl.getOffsetAndNumCandidates(int32(len(potentialNodes)))
	if klog.V(5).Enabled() {
		var sample []string
		for i := offset; i < offset+10 && i < int32(len(potentialNodes)); i++ {
			sample = append(sample, potentialNodes[i].Node().Name)
		}
		klog.Infof("from a pool of %d nodes (offset: %d, sample %d nodes: %v), ~%d candidates will be chosen", len(potentialNodes), offset, len(sample), sample, numCandidates)
	}

	return pl.dryRunPreemption(ctx, pl.fh.PreemptHandle(), state, preemptor, potentialNodes, pdbs, offset, numCandidates), nil
}

// dryRunPreemption simulates Preemption logic on <potentialNodes> in parallel,
// and returns preemption candidates.
// This is almost identical to DefaultPreemption plugin's one.
func (pl *PreemptionToleration) dryRunPreemption(ctx context.Context, fh framework.PreemptHandle,
	state *framework.CycleState, preemptor *v1.Pod, potentialNodes []*framework.NodeInfo,
	pdbs []*policy.PodDisruptionBudget, offset int32, numCandidates int32) []dp.Candidate {
	nonViolatingCandidates := newCandidateList(numCandidates)
	violatingCandidates := newCandidateList(numCandidates)
	parallelCtx, cancel := context.WithCancel(ctx)
	now := pl.clock.Now()

	checkNode := func(i int) {
		nodeInfoCopy := potentialNodes[(int(offset)+i)%len(potentialNodes)].Clone()
		stateCopy := state.Clone()
		pods, numPDBViolations, fits := pl.selectVictimsOnNode(ctx, fh, stateCopy, preemptor, nodeInfoCopy, pdbs, now)
		if fits {
			victims := extenderv1.Victims{
				Pods:             pods,
				NumPDBViolations: int64(numPDBViolations),
			}
			c := &candidate{
				victims: &victims,
				name:    nodeInfoCopy.Node().Name,
			}
			if numPDBViolations == 0 {
				nonViolatingCandidates.add(c)
			} else {
				violatingCandidates.add(c)
			}
			nvcSize, vcSize := nonViolatingCandidates.size(), violatingCandidates.size()
			if nvcSize > 0 && nvcSize+vcSize >= numCandidates {
				cancel()
			}
		}
	}
	Until(parallelCtx, len(potentialNodes), checkNode)
	return append(nonViolatingCandidates.get(), violatingCandidates.get()...)
}

// selectVictimsOnNode finds minimum set of pods on the given node that should
// be preempted in order to make enough room for "preemptor" to be scheduled.
// The algorithm is almost identical to DefaultPreemption plugin's one.
// The only difference is that it takes PreemptionToleration annotations in
// PriorityClass resources into account for selecting victim pods.
func (pl *PreemptionToleration) selectVictimsOnNode(
	ctx context.Context,
	ph framework.PreemptHandle,
	state *framework.CycleState,
	pod *v1.Pod,
	nodeInfo *framework.NodeInfo,
	pdbs []*policy.PodDisruptionBudget,
	now time.Time,
) ([]*v1.Pod, int, bool) {
	var potentialVictims []*v1.Pod

	removePod := func(rp *v1.Pod) error {
		if err := nodeInfo.RemovePod(rp); err != nil {
			return err
		}
		status := ph.RunPreFilterExtensionRemovePod(ctx, state, pod, rp, nodeInfo)
		if !status.IsSuccess() {
			return status.AsError()
		}
		return nil
	}
	addPod := func(ap *v1.Pod) error {
		nodeInfo.AddPod(ap)
		status := ph.RunPreFilterExtensionAddPod(ctx, state, pod, ap, nodeInfo)
		if !status.IsSuccess() {
			return status.AsError()
		}
		return nil
	}

	// As the first step, remove all lower priority pods that can't tolerate preemption by the preemptor
	// from the node and check if the given pod can be scheduled.
	podPriority := corev1helpers.PodPriority(pod)
	for _, p := range nodeInfo.Pods {
		canToleratePreemption, err := CanToleratePreemption(p.Pod, pod, pl.priorityClassLister, now)
		if err != nil {
			klog.Warningf("Encountered error while selecting victims on node %v: %v", nodeInfo.Node().Name, err)
			return nil, 0, false
		}
		if corev1helpers.PodPriority(p.Pod) < podPriority && !canToleratePreemption {
			potentialVictims = append(potentialVictims, p.Pod)
			if err := removePod(p.Pod); err != nil {
				return nil, 0, false
			}
		}
	}

	// No potential victims are found, and so we don't need to evaluate the node again since its state didn't change.
	if len(potentialVictims) == 0 {
		return nil, 0, false
	}

	// If the new pod does not fit after removing all the lower priority pods,
	// we are almost done and this node is not suitable for preemption. The only
	// condition that we could check is if the "pod" is failing to schedule due to
	// inter-pod affinity to one or more victims, but we have decided not to
	// support this case for performance reasons. Having affinity to lower
	// priority pods is not a recommended configuration anyway.
	if fits, _, err := core.PodPassesFiltersOnNode(ctx, ph, state, pod, nodeInfo); !fits {
		if err != nil {
			klog.Warningf("Encountered error while selecting victims on node %v: %v", nodeInfo.Node().Name, err)
		}

		return nil, 0, false
	}
	var victims []*v1.Pod
	numViolatingVictim := 0
	sort.Slice(potentialVictims, func(i, j int) bool { return util.MoreImportantPod(potentialVictims[i], potentialVictims[j]) })
	// Try to reprieve as many pods as possible. We first try to reprieve the PDB
	// violating victims and then other non-violating ones. In both cases, we start
	// from the highest priority victims.
	violatingVictims, nonViolatingVictims := filterPodsWithPDBViolation(potentialVictims, pdbs)
	reprievePod := func(p *v1.Pod) (bool, error) {
		if err := addPod(p); err != nil {
			return false, err
		}
		fits, _, _ := core.PodPassesFiltersOnNode(ctx, ph, state, pod, nodeInfo)
		if !fits {
			if err := removePod(p); err != nil {
				return false, err
			}
			victims = append(victims, p)
			klog.V(5).Infof("Pod %v/%v is a potential preemption victim on node %v.", p.Namespace, p.Name, nodeInfo.Node().Name)
		}
		return fits, nil
	}
	for _, p := range violatingVictims {
		if fits, err := reprievePod(p); err != nil {
			klog.Warningf("Failed to reprieve pod %q: %v", p.Name, err)
			return nil, 0, false
		} else if !fits {
			numViolatingVictim++
		}
	}
	// Now we try to reprieve non-violating victims.
	for _, p := range nonViolatingVictims {
		if _, err := reprievePod(p); err != nil {
			klog.Warningf("Failed to reprieve pod %q: %v", p.Name, err)
			return nil, 0, false
		}
	}
	return victims, numViolatingVictim, true
}
