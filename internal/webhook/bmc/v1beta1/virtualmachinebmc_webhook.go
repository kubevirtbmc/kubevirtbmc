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

package v1beta1

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubevirtv1 "kubevirt.io/api/core/v1"
	bmcv1beta1 "kubevirt.io/kubevirtbmc/api/bmc/v1beta1"
)

// nolint:unused
// log is for logging in this package.
var virtualmachinebmclog = logf.Log.WithName("virtualmachinebmc-resource")

// SetupVirtualMachineBMCWebhookWithManager registers the webhook for VirtualMachineBMC in the manager.
func SetupVirtualMachineBMCWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&bmcv1beta1.VirtualMachineBMC{}).
		WithValidator(&VirtualMachineBMCCustomValidator{
			Client: mgr.GetClient(),
		}).
		WithDefaulter(&VirtualMachineBMCCustomDefaulter{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-bmc-kubevirt-io-v1beta1-virtualmachinebmc,mutating=true,failurePolicy=fail,sideEffects=None,groups=bmc.kubevirt.io,resources=virtualmachinebmcs,verbs=create;update,versions=v1beta1,name=mvirtualmachinebmc-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualMachineBMCCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind VirtualMachineBMC when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type VirtualMachineBMCCustomDefaulter struct {
	// TODO(user): Add more fields as needed for defaulting
}

var _ webhook.CustomDefaulter = &VirtualMachineBMCCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind VirtualMachineBMC.
func (d *VirtualMachineBMCCustomDefaulter) Default(_ context.Context, obj runtime.Object) error {
	virtualmachinebmc, ok := obj.(*bmcv1beta1.VirtualMachineBMC)

	if !ok {
		return fmt.Errorf("expected an VirtualMachineBMC object but got %T", obj)
	}
	virtualmachinebmclog.Info("Defaulting for VirtualMachineBMC", "name", virtualmachinebmc.GetName())

	// TODO(user): fill in your defaulting logic.

	return nil
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-bmc-kubevirt-io-v1beta1-virtualmachinebmc,mutating=false,failurePolicy=fail,sideEffects=None,groups=bmc.kubevirt.io,resources=virtualmachinebmcs,verbs=create;update,versions=v1beta1,name=vvirtualmachinebmc-v1beta1.kb.io,admissionReviewVersions=v1

// VirtualMachineBMCCustomValidator struct is responsible for validating the VirtualMachineBMC resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type VirtualMachineBMCCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &VirtualMachineBMCCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type VirtualMachineBMC.
func (v *VirtualMachineBMCCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualmachinebmc, ok := obj.(*bmcv1beta1.VirtualMachineBMC)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineBMC object but got %T", obj)
	}
	virtualmachinebmclog.Info("Validation for VirtualMachineBMC upon creation", "name", virtualmachinebmc.GetName())

	if err := v.validateVirtualMachineRef(ctx, virtualmachinebmc); err != nil {
		return nil, err
	}

	if err := v.validateAuthSecretRef(ctx, virtualmachinebmc); err != nil {
		return nil, err
	}

	return v.validateNetworkRef(ctx, virtualmachinebmc)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type VirtualMachineBMC.
func (v *VirtualMachineBMCCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	virtualmachinebmc, ok := newObj.(*bmcv1beta1.VirtualMachineBMC)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineBMC object for the newObj but got %T", newObj)
	}
	oldVirtualmachinebmc, ok := oldObj.(*bmcv1beta1.VirtualMachineBMC)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineBMC object for the oldObj but got %T", oldObj)
	}
	virtualmachinebmclog.Info("Validation for VirtualMachineBMC upon update", "name", virtualmachinebmc.GetName())

	if err := v.validateVirtualMachineRef(ctx, virtualmachinebmc); err != nil {
		return nil, err
	}

	if err := v.validateAuthSecretRef(ctx, virtualmachinebmc); err != nil {
		return nil, err
	}

	// Don't repeat warnings on updates that leave networkRef alone.
	warnings, err := v.validateNetworkRef(ctx, virtualmachinebmc)
	if err != nil || virtualmachinebmc.Spec.NetworkRef == oldVirtualmachinebmc.Spec.NetworkRef {
		return nil, err
	}
	return warnings, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type VirtualMachineBMC.
func (v *VirtualMachineBMCCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	virtualmachinebmc, ok := obj.(*bmcv1beta1.VirtualMachineBMC)
	if !ok {
		return nil, fmt.Errorf("expected a VirtualMachineBMC object but got %T", obj)
	}
	virtualmachinebmclog.Info("Validation for VirtualMachineBMC upon deletion", "name", virtualmachinebmc.GetName())

	// No validation needed for deletion
	return nil, nil
}

// validateVirtualMachineRef checks if the referenced VirtualMachine exists in the same namespace.
func (v *VirtualMachineBMCCustomValidator) validateVirtualMachineRef(ctx context.Context, virtualmachinebmc *bmcv1beta1.VirtualMachineBMC) error {
	if virtualmachinebmc.Spec.VirtualMachineRef == nil {
		return fmt.Errorf("spec.virtualMachineRef is required")
	}

	if virtualmachinebmc.Spec.VirtualMachineRef.Name == "" {
		return fmt.Errorf("spec.virtualMachineRef.name cannot be empty")
	}

	vmKey := client.ObjectKey{
		Namespace: virtualmachinebmc.Namespace,
		Name:      virtualmachinebmc.Spec.VirtualMachineRef.Name,
	}

	var vm kubevirtv1.VirtualMachine
	if err := v.Client.Get(ctx, vmKey, &vm); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("referenced VirtualMachine %q not found in namespace %q",
				virtualmachinebmc.Spec.VirtualMachineRef.Name,
				virtualmachinebmc.Namespace)
		}
		return fmt.Errorf("error checking VirtualMachine existence: %w", err)
	}

	return nil
}

// validateAuthSecretRef checks if the referenced Secret exists in the same namespace.
func (v *VirtualMachineBMCCustomValidator) validateAuthSecretRef(ctx context.Context, virtualmachinebmc *bmcv1beta1.VirtualMachineBMC) error {
	if virtualmachinebmc.Spec.AuthSecretRef == nil {
		return fmt.Errorf("spec.authSecretRef is required")
	}

	if virtualmachinebmc.Spec.AuthSecretRef.Name == "" {
		return fmt.Errorf("spec.authSecretRef.name cannot be empty")
	}

	secretKey := client.ObjectKey{
		Namespace: virtualmachinebmc.Namespace,
		Name:      virtualmachinebmc.Spec.AuthSecretRef.Name,
	}

	var secret corev1.Secret
	if err := v.Client.Get(ctx, secretKey, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("referenced Secret %q not found in namespace %q",
				virtualmachinebmc.Spec.AuthSecretRef.Name,
				virtualmachinebmc.Namespace)
		}
		return fmt.Errorf("error checking Secret existence: %w", err)
	}

	return nil
}

// validateNetworkRef only warns about a missing NAD, since it may be created later.
func (v *VirtualMachineBMCCustomValidator) validateNetworkRef(ctx context.Context, virtualmachinebmc *bmcv1beta1.VirtualMachineBMC) (admission.Warnings, error) {
	nadNamespace, nadName, ok := virtualmachinebmc.Spec.NetworkRefNamespacedName(virtualmachinebmc.Namespace)
	if !ok {
		return nil, nil
	}

	if strings.Contains(nadName, "/") {
		return nil, fmt.Errorf("spec.networkRef %q must be a NetworkAttachmentDefinition name or namespace/name", virtualmachinebmc.Spec.NetworkRef)
	}
	if errs := validation.IsDNS1123Label(nadNamespace); len(errs) > 0 {
		return nil, fmt.Errorf("spec.networkRef %q has an invalid namespace: %s", virtualmachinebmc.Spec.NetworkRef, strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Subdomain(nadName); len(errs) > 0 {
		return nil, fmt.Errorf("spec.networkRef %q has an invalid name: %s", virtualmachinebmc.Spec.NetworkRef, strings.Join(errs, "; "))
	}

	nadKey := nadNamespace + "/" + nadName
	var warnings admission.Warnings
	if nadNamespace != virtualmachinebmc.Namespace {
		warnings = append(warnings, fmt.Sprintf("spec.networkRef references NetworkAttachmentDefinition %q in another namespace; "+
			"enable Multus namespaceIsolation to restrict cross-namespace network attachments", nadKey))
	}

	nad := &unstructured.Unstructured{}
	nad.SetGroupVersionKind(bmcv1beta1.NetworkAttachmentDefinitionGVK)
	err := v.Client.Get(ctx, client.ObjectKey{Namespace: nadNamespace, Name: nadName}, nad)
	switch {
	case err == nil:
	case meta.IsNoMatchError(err):
		warnings = append(warnings, "NetworkAttachmentDefinition CRD not found; Multus must be installed for spec.networkRef to take effect")
	case apierrors.IsNotFound(err):
		warnings = append(warnings, fmt.Sprintf("NetworkAttachmentDefinition %q not found; the virtBMC Pod will not start until it exists", nadKey))
	default:
		virtualmachinebmclog.Error(err, "unable to get NetworkAttachmentDefinition", "networkAttachmentDefinition", nadKey)
	}

	return warnings, nil
}
