// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Authors of Nimbus

package manager

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	intentv1 "github.com/5GSEC/nimbus/api/v1"
	"github.com/5GSEC/nimbus/pkg/adapter/common"
	"github.com/5GSEC/nimbus/pkg/adapter/k8s"
	processor "github.com/5GSEC/nimbus/pkg/adapter/nimbus-coco/processor"
	podwatcher "github.com/5GSEC/nimbus/pkg/adapter/nimbus-coco/watcher"
	adapterutil "github.com/5GSEC/nimbus/pkg/adapter/util"
	globalwatcher "github.com/5GSEC/nimbus/pkg/adapter/watcher"
	"github.com/go-logr/logr"
)

var (
	scheme    = runtime.NewScheme()
	k8sClient client.Client
)

func init() {
	utilruntime.Must(intentv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
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
			reconcileDeploy(ctx, np.Name, np.Namespace, podCh)
		case deletedNp := <-deletedNpCh:
			deleteDeploy(ctx, deletedNp.Name, deletedNp.Namespace)
		case _ = <-clusterNpChan:
			fmt.Println("No-op for ClusterNimbusPolicy")
		case _ = <-deletedClusterNpChan:
			fmt.Println("No-op for ClusterNimbusPolicy")
		}
	}
}

func reconcileDeploy(ctx context.Context, npName, npNamespace string, podCh chan common.Request) {
	logger := log.FromContext(ctx)

	np, err := getNP(ctx, logger, npName, npNamespace)
	if err != nil {
		logger.Error(err, "error getting NimbusPolicy")
		return
	}

	if adapterutil.IsOrphan(np.GetOwnerReferences(), "SecurityIntentBinding") {
		logger.V(4).Info("ignoring orphan NimbusPolicy", "NimbusPolicy.Name", npName, "NimbusPolicy.Namespace", npNamespace)
		return
	}

	deployments, err := listDeploy(ctx, np.Spec.Selector.MatchLabels)
	if err != nil {
		logger.Error(err, "error listing deployments")
		return
	}

	if len(deployments) == 0 {
		logger.Info("Deployment not found, wait for a matching pod to appear")
		go WaitForMatching(ctx, podCh, np)
	} else {
		for _, deployment := range deployments {
			if isNonCVM(&deployment) {
				updateDeployToCVM(ctx, logger, &deployment, np)
			} else {
				logger.Info("Deployment is already running on CVM", "Deployment.Name", deployment.Name)
				updateDeployMetadata(ctx, logger, &deployment, np)
			}
		}
	}
}

func WaitForMatching(ctx context.Context, podCh chan common.Request, np *intentv1.NimbusPolicy) {
	logger := log.FromContext(ctx)
	var stopLoop bool

	for {
		if stopLoop {
			return
		}
		select {
		case <-ctx.Done():
			return
		case podReq := <-podCh:
			pod, err := getPod(ctx, podReq.Name, podReq.Namespace)
			if err != nil {
				if errors.IsNotFound(err) {
					logger.V(1).Info("Pod not found, it might have been deleted", "Pod.Name", podReq.Name, "Namespace", podReq.Namespace)
				} else {
					logger.Error(err, "failed to fetch pod details", "Pod.Name", podReq.Name)
				}
				continue
			}
			if checkLabelMatch(pod.Labels, np.Spec.Selector.MatchLabels) {
				logger.Info("K8s Pod found", "Pod.Name", pod.Name, "Pod.Namespace", pod.Namespace)
				deployment, err := getDeployFromPod(ctx, pod)
				if err != nil {
					if errors.IsNotFound(err) {
						createDeployFromPod(ctx, logger, pod, np)
						deletePod(ctx, logger, pod)
						stopLoop = true
					} else {
						logger.Error(err, "failed to fetch deployment details", "Pod.Name", podReq.Name)
					}
					continue
				}
				if isNonCVM(deployment) {
					updateDeployToCVM(ctx, logger, deployment, np)
					stopLoop = true
				} else {
					logger.Info("Deployment is already running on CVM", "Deployment.Name", deployment.Name)
					updateDeployMetadata(ctx, logger, deployment, np)
					stopLoop = true
				}
			}
		}
	}
}

func createDeployFromPod(ctx context.Context, logger logr.Logger, pod *corev1.Pod, np *intentv1.NimbusPolicy) {
	logger.Info("Create new Deployment for Pod", "Pod.Name", pod.Name, "Pod.Namespace", pod.Namespace)

	newDeployment := processor.BuildDeployFromPod(pod, np)

	if err := ctrl.SetControllerReference(np, &newDeployment, scheme); err != nil {
		logger.Error(err, "failed to set OwnerReference on new Deployment", "Deployment.Name", newDeployment.Name, "Deployment.Namespace", newDeployment.Namespace)
		return
	}

	if err := k8sClient.Create(ctx, &newDeployment); err != nil {
		logger.Error(err, "failed to create new Deployment", "Deployment.Name", newDeployment.Name)
		return
	}
	logger.Info("Successfully created new Deployment", "Deployment.Name", newDeployment.Name)
}

func updateDeployToCVM(ctx context.Context, logger logr.Logger, oldDeployment *appsv1.Deployment, np *intentv1.NimbusPolicy) {
	newDeployments := processor.BuildDeployFromCVM(logger, np, oldDeployment)

	for _, newDeployment := range newDeployments {
		if err := ctrl.SetControllerReference(np, &newDeployment, scheme); err != nil {
			logger.Error(err, "failed to set OwnerReference on Deployment", "Deployment.Name", newDeployment.Name, "Deployment.Namespace", newDeployment.Namespace)
			return
		}

		if err := k8sClient.Update(ctx, &newDeployment); err != nil {
			logger.Error(err, "failed to update CVM Deployment", "Deployment.Name", newDeployment.Name)
		} else {
			logger.Info("Successfully updated CVM Deployment", "Deployment.Name", newDeployment.Name)
		}
	}
}

func updateDeployMetadata(ctx context.Context, logger logr.Logger, deployment *appsv1.Deployment, np *intentv1.NimbusPolicy) {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Fetch the latest version of Deployment before attempting update
		var latestDeployment appsv1.Deployment
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: deployment.Name, Namespace: deployment.Namespace}, &latestDeployment); err != nil {
			return err
		}

		if err := ctrl.SetControllerReference(np, &latestDeployment, scheme); err != nil {
			logger.Error(err, "failed to set OwnerReference on Deployment", "Deployment.Name", latestDeployment.Name, "Deployment.Namespace", latestDeployment.Namespace)
			return err
		}

		processor.AddManagedByAnnotation(&latestDeployment)

		if err := k8sClient.Update(ctx, &latestDeployment); err != nil {
			return err
		}

		logger.Info("Successfully updated Deployment with Metadata", "Deployment.Name", latestDeployment.Name)
		return nil
	})

	if retryErr != nil {
		logger.Error(retryErr, "failed to update Deployment with Metadata after retries", "Deployment.Name", deployment.Name)
	}
}

func deletePod(ctx context.Context, logger logr.Logger, pod *corev1.Pod) {
	err := k8sClient.Delete(ctx, pod)
	if err != nil {
		logger.Error(err, "failed to delete Pod", "Pod.Name", pod.Name)
	} else {
		logger.Info("Successfully deleted Pod", "Pod.Name", pod.Name)
	}
}

func deleteDeploy(ctx context.Context, npName, namespace string) {
	logger := log.FromContext(ctx)

	// NimbusPolicy가 실제로 삭제되기 전에 CVM 디플로이먼트 정보를 가져와서 캐시에 저장합니다.
	np, err := getNP(ctx, logger, npName, namespace)
	if err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "failed to get NimbusPolicy", "NimbusPolicy.Name", npName, "NimbusPolicy.Namespace", namespace)
		}
		return
	}

	deployments, err := listDeploy(ctx, np.Spec.Selector.MatchLabels)
	if err != nil {
		logger.Error(err, "failed to list Deployments for NimbusPolicy", "NimbusPolicy.Name", npName)
		return
	}

	for _, deployment := range deployments {
		if isRunningOnCVM(&deployment) {
			newDeployment := processor.BuildDeployFromK8s(logger, deployment)
			if err := k8sClient.Create(ctx, &newDeployment); err != nil {
				logger.Error(err, "failed to create normal Deployment from CVM Deployment", "Deployment.Name", deployment.Name)
			} else {
				logger.Info("Successfully created normal Deployment from CVM Deployment", "Old Deployment.Name", deployment.Name, "New Deployment.Name", newDeployment.Name)
			}
		}
	}

	// NimbusPolicy 삭제
	if err := k8sClient.Delete(ctx, np); err != nil {
		logger.Error(err, "failed to delete NimbusPolicy", "NimbusPolicy.Name", npName)
	}
}
