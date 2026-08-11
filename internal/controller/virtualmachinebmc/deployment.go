/*
Copyright 2024.

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

package virtualmachinebmc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bmcv1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

const (
	defaultDesiredReplicas int32 = 1
	fieldManager                 = "kubevirtbmc-controller"
)

func (r *VirtualMachineBMCReconciler) createVirtBMCDeployment(virtualMachineBMC *bmcv1.VirtualMachineBMC, secretHash string) *appsv1.Deployment {

	serviceAccountName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)
	deploymentName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)

	var secretName string
	if virtualMachineBMC.Spec.AuthSecretRef != nil {
		secretName = virtualMachineBMC.Spec.AuthSecretRef.Name
	}

	labels := map[string]string{
		bmcv1.VirtualMachineBMCNameLabel: virtualMachineBMC.Name,
		bmcv1.VMNameLabel:                virtualMachineBMC.Spec.VirtualMachineRef.Name,
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: virtualMachineBMC.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(defaultDesiredReplicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: func() map[string]string {
						annotations := map[string]string{
							EnableIPMIAnnotation: strconv.FormatBool(specIPMIEnabled(&virtualMachineBMC.Spec)),
						}
						if secretHash != "" {
							annotations[SecretHashAnnotation] = secretHash
						}
						return annotations
					}(),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccountName,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:  virtBMCContainerName,
							Image: fmt.Sprintf("%s:%s", r.AgentImageName, r.AgentImageTag),
							Args: func() []string {
								args := []string{
									"--address",
									"0.0.0.0",
									"--redfish-port",
									strconv.Itoa(redfishPort),
								}
								if specIPMIEnabled(&virtualMachineBMC.Spec) {
									args = append(args, "--enable-ipmi", "--ipmi-port", strconv.Itoa(ipmiPort))
								}
								args = append(args, virtualMachineBMC.Namespace, virtualMachineBMC.Spec.VirtualMachineRef.Name)
								return args
							}(),
							Ports: func() []corev1.ContainerPort {
								ports := []corev1.ContainerPort{
									{
										Name:          redfishPortName,
										ContainerPort: redfishPort,
										Protocol:      corev1.ProtocolTCP,
									},
								}
								if specIPMIEnabled(&virtualMachineBMC.Spec) {
									ports = append(ports, corev1.ContainerPort{
										Name:          ipmiPortName,
										ContainerPort: ipmiPort,
										Protocol:      corev1.ProtocolUDP,
									})
								}
								return ports
							}(),
							Env: []corev1.EnvVar{
								{
									Name: "BMC_USERNAME",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: secretName,
											},
											Key: bmcUserKey,
										},
									},
								},
								{
									Name: "BMC_PASSWORD",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: secretName,
											},
											Key: bmcPasswordKey,
										},
									},
								},
								{
									Name: "POD_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.name",
										},
									},
								},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/redfish/v1",
										Port: intstr.FromString(redfishPortName),
									},
								},
								InitialDelaySeconds: 2,
								PeriodSeconds:       10,
							},
							Resources: corev1.ResourceRequirements{
								Requests: r.agentResourceRequests(),
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								ReadOnlyRootFilesystem:   ptr.To(true),
								RunAsNonRoot:             ptr.To(true),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
						},
					},
				},
			},
		},
	}
	return deployment
}

func (r *VirtualMachineBMCReconciler) authSecretHash(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) (string, error) {
	if virtualMachineBMC.Spec.AuthSecretRef == nil {
		return "", nil
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name:      virtualMachineBMC.Spec.AuthSecretRef.Name,
		Namespace: virtualMachineBMC.Namespace,
	}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	h := sha256.New()
	h.Write(secret.Data[bmcUserKey])
	h.Write(secret.Data[bmcPasswordKey])
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (r *VirtualMachineBMCReconciler) ensureVirtBMCDeployment(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) error {
	log := log.FromContext(ctx)

	secretHash, err := r.authSecretHash(ctx, virtualMachineBMC)
	if err != nil {
		return err
	}

	desired := r.createVirtBMCDeployment(virtualMachineBMC, secretHash)
	desired.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
	if err := ctrl.SetControllerReference(virtualMachineBMC, desired, r.Scheme); err != nil {
		return err
	}

	if err := r.Patch(ctx, desired, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		log.Error(err, "unable to apply Deployment for VirtualMachineBMC", "deployment", desired.Name)
		return err
	}

	log.V(1).Info("applied Deployment for VirtualMachineBMC", "deployment", desired.Name)
	return nil
}

func (r *VirtualMachineBMCReconciler) deleteVirtBMCDeployment(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) error {
	log := log.FromContext(ctx)
	deployment, err := r.getVirtBMCDeployment(ctx, virtualMachineBMC)
	if err != nil {
		return err
	}

	if deployment == nil {
		return nil
	}

	if err := r.Delete(ctx, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		log.Error(err, "unable to delete virtBMC Deployment", "deployment", deployment)
		return err
	}

	log.Info("deleted virtBMC Deployment", "deployment", deployment)
	return nil
}

func (r *VirtualMachineBMCReconciler) getVirtBMCDeployment(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) (*appsv1.Deployment, error) {
	log := log.FromContext(ctx)
	deploymentName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)

	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: virtualMachineBMC.Namespace,
	}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		log.Error(err, "unable to get virtBMC Deployment", "deployment", deploymentName)
		return nil, err
	}

	return deployment, nil
}

// Note: This function needs to be cleaned up in future releases once we are confident that all users have
// upgraded to the newer release.
func (r *VirtualMachineBMCReconciler) deleteLegacyVirtBMCPod(ctx context.Context, virtualMachineBMC *bmcv1.VirtualMachineBMC) error {
	log := log.FromContext(ctx)
	podName := fmt.Sprintf("%s-virtbmc", virtualMachineBMC.Spec.VirtualMachineRef.Name)

	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: virtualMachineBMC.Namespace}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if !metav1.IsControlledBy(pod, virtualMachineBMC) {
		return nil
	}

	if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		log.Error(err, "unable to delete legacy virtBMC Pod", "pod", podName)
		return err
	}

	log.Info("deleted legacy virtBMC Pod", "pod", podName)
	return nil
}
