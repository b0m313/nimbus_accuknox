// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Authors of Nimbus

package manager

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	intentv1 "github.com/5GSEC/nimbus/api/v1"
	"github.com/5GSEC/nimbus/pkg/adapter/common"
	"github.com/5GSEC/nimbus/pkg/adapter/k8s"
	processor "github.com/5GSEC/nimbus/pkg/adapter/nimbus-coco/processor"
	podwatcher "github.com/5GSEC/nimbus/pkg/adapter/nimbus-coco/watcher"
	adapterutil "github.com/5GSEC/nimbus/pkg/adapter/util"
	globalwatcher "github.com/5GSEC/nimbus/pkg/adapter/watcher"
)

var (
	scheme    = runtime.NewScheme()
	k8sClient client.Client
)

func init() {
	utilruntime.Must(intentv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	k8sClient = k8s.NewOrDie(scheme)
}

func Run(ctx context.Context) {
	npCh := make(chan common.Request)
	deletedNpCh := make(chan common.Request)
	go globalwatcher.WatchNimbusPolicies(ctx, npCh, deletedNpCh)

	clusterNpChan := make(chan string)
	deletedClusterNpChan := make(chan string)
	go globalwatcher.WatchClusterNimbusPolicies(ctx, clusterNpChan, deletedClusterNpChan)

	podCh := make(chan common.Request)
	go podwatcher.WatchPods(ctx, podCh)

	for {
		select {
		case <-ctx.Done():
			close(npCh)
			close(deletedNpCh)
			close(clusterNpChan)
			close(deletedClusterNpChan)
			close(podCh)
			return
		case np := <-npCh:
			createOrUpdatePod(ctx, np.Name, np.Namespace, podCh)
		case deletedNp := <-deletedNpCh:
			deletePod(ctx, deletedNp.Name, deletedNp.Namespace)
		case _ = <-clusterNpChan: // Fixme
			fmt.Println("No-op for ClusterNimbusPolicy")
		case _ = <-deletedClusterNpChan: // Fixme
			fmt.Println("No-op for ClusterNimbusPolicy")
		}
	}
}

// NP에 따라 파드를 생성하거나 업데이트
func createOrUpdatePod(ctx context.Context, npName, npNamespace string, podCh chan common.Request) {
	logger := log.FromContext(ctx)

	np, err := getNP(ctx, npName, npNamespace)
	if err != nil {
		logger.Error(err, "error geting NimbusPolicy")
		return
	}

	if adapterutil.IsOrphan(np.GetOwnerReferences(), "SecurityIntentBinding") {
		logger.V(4).Info("Ignoring orphan NimbusPolicy", "NimbusPolicy.Name", npName, "NimbusPolicy.Namespace", npNamespace)
		return
	}

	pods, err := listPodsBySelector(ctx, np.Spec.Selector.MatchLabels)
	if err != nil {
		logger.Error(err, "error listing pods")
		return
	}

	if len(pods) == 0 {
		logger.Info("Pod not found, wait for a matching pod to appear")
		go waitForMatchingPods(ctx, podCh, np)
	} else {
		for _, pod := range pods {
			if shouldTransformToKata(&pod) {
				createPodInKata(ctx, &pod, np)
				deleteDanglingPod(ctx, &pod)
			} else if isKataPod(&pod) {
				logger.Info("Pod is already running on CVM pod", "Pod.Name", pod.Name)
			}
		}
	}
}

func waitForMatchingPods(ctx context.Context, podCh chan common.Request, np *intentv1.NimbusPolicy) {
	logger := log.FromContext(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case podReq := <-podCh:
			pod, err := getPod(ctx, podReq.Name, podReq.Namespace)
			if err != nil {
				logger.Error(err, "failed to fetch pod details", "Pod.Name", podReq.Name)
				continue
			}
			if matchesPolicyLabels(pod.Labels, np.Spec.Selector.MatchLabels) {
				logger.Info("K8s Pod found", "Pod.Name", pod.Name, "Pod.Namespace", pod.Namespace)
				if isKataPod(pod) {
					logger.Info("Pod is already running on the desired CVM runtime", "Pod.Name", pod.Name)
					return
				} else if shouldTransformToKata(pod) {
					logger.Info("Transforming K8s Pod to CVM Pod", "Pod.Name", pod.Name)
					createPodInKata(ctx, pod, np)
					deleteDanglingPod(ctx, pod)
				}
			}
		}
	}
}

// 주어진 파드를 기반으로 kata container pod를 생성
func createPodInKata(ctx context.Context, oldPod *corev1.Pod, np *intentv1.NimbusPolicy) {
	logger := log.FromContext(ctx)
	newPods := processor.BuildpodsFromKata(logger, np, oldPod)

	for _, newPod := range newPods {
		// Set NimbusPolicy as the owner of the pod
		//if err := ctrl.SetControllerReference(np, &newPod, scheme); err != nil {
		//	logger.Error(err, "failed to set OwnerReference on Pod", "Pod.Name", newPod.Name, "Pod.Namespace", newPod.Namespace)
		//	return
		//}

		// Check if the pod already exists
		var existingPod corev1.Pod
		err := k8sClient.Get(ctx, types.NamespacedName{Name: newPod.Name, Namespace: newPod.Namespace}, &existingPod)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "failed to check if CVM Pod already exists", "Pod.Name", newPod.Name)
			return
		}
		if err == nil {
			continue // Skip creation if pod already exists
		}

		// If not found, create the new pod
		if err := k8sClient.Create(ctx, &newPod); err != nil {
			logger.Error(err, "failed to create CVM Pod", "Pod.Name", newPod.Name)
			continue
		}
		logger.Info("Successfully created CVM Pod", "Pod.Name", newPod.Name)
	}
}

func createPodInK8s(ctx context.Context, oldPod corev1.Pod) *corev1.Pod {
	logger := log.FromContext(ctx)

	// 기존 파드 정보를 기반으로 새로운 일반 파드를 생성합니다.
	newPod := processor.BuildpodsFromK8s(logger, oldPod)

	// 새로운 일반 파드를 생성합니다.
	if err := k8sClient.Create(ctx, &newPod); err != nil {
		logger.Error(err, "Failed to create K8s Pod", "Pod.Name", newPod.Name)
		return nil
	}
	logger.Info("Successfully created K8s Pod", "Pod.Name", newPod.Name)
	return &newPod
}

func deletePod(ctx context.Context, npName, namespace string) {
	logger := log.FromContext(ctx)

	// NimbusPolicy에 연결된 CVM 파드들을 필터링하여 조회합니다.
	labelSelector := labels.SelectorFromSet(map[string]string{"app.kubernetes.io/managed-by": "nimbus-coco"})
	listOpts := client.ListOptions{Namespace: namespace, LabelSelector: labelSelector}
	var pods corev1.PodList
	if err := k8sClient.List(ctx, &pods, &listOpts); err != nil {
		logger.Error(err, "Failed to list Pods for NimbusPolicy", "NimbusPolicy.Name", npName)
		return
	}

	// 조회된 CVM 파드들을 일반 파드로 전환합니다.
	for _, pod := range pods.Items {
		if isKataPod(&pod) {
			// CVM 파드를 일반 파드로 전환하기 위해 새로운 파드 생성
			newPod := createPodInK8s(ctx, pod)
			if newPod != nil {
				// 새로운 파드 생성 후 기존 CVM 파드 삭제
				if err := k8sClient.Delete(ctx, &pod); err != nil {
					logger.Error(err, "Failed to delete CVM Pod", "Pod.Name", pod.Name)
				} else {
					logger.Info("Successfully deleted CVM Pod and created normal Pod", "Old Pod.Name", pod.Name, "New Pod.Name", newPod.Name)
				}
			}
		}
	}
}

func deleteDanglingPod(ctx context.Context, pod *corev1.Pod) {
	logger := log.FromContext(ctx)
	err := k8sClient.Delete(ctx, pod)
	if err != nil {
		logger.Error(err, "failed to delete K8s Pod")
	}
	logger.Info("K8s Pod deleted successfully", "Pod.Name", pod.Name)
}

func shouldTransformToKata(pod *corev1.Pod) bool {
	return pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "kata-qemu-snp"
}

func isKataPod(pod *corev1.Pod) bool {
	return pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName == "kata-qemu-snp"
}

func matchesPolicyLabels(podLabels, policyLabels map[string]string) bool {
	for k, v := range policyLabels {
		if podLabels[k] != v {
			return false
		}
	}
	return true
}

// 주어진 셀렉터로 파드를 조회
func listPodsBySelector(ctx context.Context, selector map[string]string) ([]corev1.Pod, error) {
	var podList corev1.PodList
	listOpts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(selector),
	}
	if err := k8sClient.List(ctx, &podList, listOpts); err != nil {
		return nil, err
	}
	return podList.Items, nil
}

func getNP(ctx context.Context, npName, namespace string) (*intentv1.NimbusPolicy, error) {
	logger := log.FromContext(ctx)
	var np intentv1.NimbusPolicy

	err := k8sClient.Get(ctx, types.NamespacedName{Name: npName, Namespace: namespace}, &np)
	if err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "failed to get NimbusPolicy", "NimbusPolicy.Name", npName, "NimbusPolicy.Namespace", namespace)
		}
		return nil, err
	}

	return &np, nil
}

func getPod(ctx context.Context, podName, namespace string) (*corev1.Pod, error) {
	logger := log.FromContext(ctx)
	var pod corev1.Pod
	err := k8sClient.Get(ctx, types.NamespacedName{Name: podName, Namespace: namespace}, &pod)
	if err != nil {
		logger.Error(err, "Error getting pod", "Pod.Name", podName, "Namespace", namespace)
		return nil, err
	}
	return &pod, nil
}
