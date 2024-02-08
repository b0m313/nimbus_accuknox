// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Authors of Nimbus

package manager

import (
	"context"
	"fmt"

	kubearmorv1 "github.com/kubearmor/KubeArmor/pkg/KubeArmorController/api/security.kubearmor.com/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	intentv1 "github.com/5GSEC/nimbus/api/v1"
	"github.com/5GSEC/nimbus/pkg/adapter/common"
	"github.com/5GSEC/nimbus/pkg/adapter/k8s"
	adapterutil "github.com/5GSEC/nimbus/pkg/adapter/util"
	globalwatcher "github.com/5GSEC/nimbus/pkg/adapter/watcher"

	"github.com/5GSEC/nimbus/pkg/adapter/nimbus-kubearmor/processor"
	kspwatcher "github.com/5GSEC/nimbus/pkg/adapter/nimbus-kubearmor/watcher"
)

var (
	scheme    = runtime.NewScheme()
	np        intentv1.NimbusPolicy
	k8sClient client.Client
)

func init() {
	utilruntime.Must(intentv1.AddToScheme(scheme))
	utilruntime.Must(kubearmorv1.AddToScheme(scheme))
	k8sClient = k8s.NewOrDie(scheme)
}

func Run(ctx context.Context) {
	npCh := make(chan common.Request)
	deletedNpCh := make(chan common.Request)
	go globalwatcher.WatchNimbusPolicies(ctx, npCh, deletedNpCh)

	clusterNpChan := make(chan string)
	deletedClusterNpChan := make(chan string)
	go globalwatcher.WatchClusterNimbusPolicies(ctx, clusterNpChan, deletedClusterNpChan)

	updatedKspCh := make(chan common.Request)
	deletedKspCh := make(chan common.Request)
	go kspwatcher.WatchKsps(ctx, updatedKspCh, deletedKspCh)

	for {
		select {
		case <-ctx.Done():
			close(npCh)
			close(deletedNpCh)
			close(clusterNpChan)
			close(deletedClusterNpChan)
			close(updatedKspCh)
			close(deletedKspCh)
			return
		case createdNp := <-npCh:
			createOrUpdateKsp(ctx, createdNp.Name, createdNp.Namespace)
		case deletedNp := <-deletedNpCh:
			deleteKsp(ctx, deletedNp.Name, deletedNp.Namespace)
		case updatedKsp := <-updatedKspCh:
			reconcileKsp(ctx, updatedKsp.Name, updatedKsp.Namespace, false)
		case deletedKsp := <-deletedKspCh:
			reconcileKsp(ctx, deletedKsp.Name, deletedKsp.Namespace, true)
		case _ = <-clusterNpChan: // Fixme: CreateKSP based on ClusterNP
			fmt.Println("No-op for ClusterNimbusPolicy")
		case _ = <-deletedClusterNpChan: // Fixme: DeleteKSP based on ClusterNP
			fmt.Println("No-op for ClusterNimbusPolicy")
		}
	}
}

func reconcileKsp(ctx context.Context, kspName, namespace string, deleted bool) {
	logger := log.FromContext(ctx)
	npName := adapterutil.ExtractNpName(kspName)
	err := k8sClient.Get(ctx, types.NamespacedName{Name: npName, Namespace: namespace}, &np)
	if err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "failed to get NimbusPolicy", "NimbusPolicy.Name", npName, "NimbusPolicy.Namespace", namespace)
		}
		return
	}
	if deleted {
		logger.Info("Reconciling deleted KubeArmorPolicy", "KubeArmorPolicy.Name", kspName, "KubeArmorPolicy.Namespace", namespace)
	} else {
		logger.Info("Reconciling modified KubeArmorPolicy", "KubeArmorPolicy.Name", kspName, "KubeArmorPolicy.Namespace", namespace)
	}
	createOrUpdateKsp(ctx, npName, namespace)
}

func createOrUpdateKsp(ctx context.Context, npName, npNamespace string) {
	logger := log.FromContext(ctx)
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: npName, Namespace: npNamespace}, &np); err != nil {
		logger.Error(err, "failed to get NimbusPolicy", "NimbusPolicy.Name", npName, "NimbusPolicy.Namespace", npNamespace)
		return
	}

	if adapterutil.IsOrphan(np.GetOwnerReferences(), "SecurityIntentBinding") {
		logger.V(4).Info("Ignoring orphan NimbusPolicy", "NimbusPolicy.Name", np.GetName(), "NimbusPolicy.Namespace", np.GetNamespace())
		return
	}

	ksps := processor.BuildKspsFrom(logger, &np)
	// Iterate using a separate index variable to avoid aliasing
	for idx := range ksps {
		ksp := ksps[idx]

		// Set NimbusPolicy as the owner of the KSP
		if err := ctrl.SetControllerReference(&np, &ksp, scheme); err != nil {
			logger.Error(err, "failed to set OwnerReference on KubeArmorPolicy", "Name", ksp.Name)
			return
		}

		var existingKsp kubearmorv1.KubeArmorPolicy
		err := k8sClient.Get(ctx, types.NamespacedName{Name: ksp.Name, Namespace: ksp.Namespace}, &existingKsp)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "failed to get existing KubeArmorPolicy", "KubeArmorPolicy.Name", ksp.Name, "KubeArmorPolicy.Namespace", ksp.Namespace)
			return
		}
		if err != nil && errors.IsNotFound(err) {
			if err = k8sClient.Create(ctx, &ksp); err != nil {
				logger.Error(err, "failed to create KubeArmorPolicy", "KubeArmorPolicy.Name", ksp.Name, "KubeArmorPolicy.Namespace", ksp.Namespace)
				return
			}
			logger.Info("KubeArmorPolicy created", "KubeArmorPolicy.Name", ksp.Name, "KubeArmorPolicy.Namespace", ksp.Namespace)
		} else {
			ksp.ObjectMeta.ResourceVersion = existingKsp.ObjectMeta.ResourceVersion
			if err = k8sClient.Update(ctx, &ksp); err != nil {
				logger.Error(err, "failed to configure existing KubeArmorPolicy", "KubeArmorPolicy.Name", existingKsp.Name, "KubeArmorPolicy.Namespace", existingKsp.Namespace)
				return
			}
			logger.Info("KubeArmorPolicy configured", "KubeArmorPolicy.Name", existingKsp.Name, "KubeArmorPolicy.Namespace", existingKsp.Namespace)
		}
	}
}

func deleteKsp(ctx context.Context, npName, npNamespace string) {
	logger := log.FromContext(ctx)
	var ksps kubearmorv1.KubeArmorPolicyList

	if err := k8sClient.List(ctx, &ksps, &client.ListOptions{Namespace: npNamespace}); err != nil {
		logger.Error(err, "failed to list KubeArmorPolicies")
		return
	}

	// Kubernetes GC automatically deletes the child when the parent/owner is
	// deleted. So, we don't need to do anything in this case since NimbusPolicy is
	// the owner and when it gets deleted corresponding KSPs will be automatically
	// deleted.
	for _, ksp := range ksps.Items {
		logger.Info("KubeArmorPolicy already deleted due to NimbusPolicy deletion",
			"KubeArmorPolicy.Name", ksp.Name, "KubeArmorPolicy.Namespace", ksp.Namespace,
			"NimbusPolicy.Name", npName, "NimbusPolicy.Namespace", npNamespace,
		)
	}
}