/*

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

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metal3v1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	"github.com/metal3-io/baremetal-operator/pkg/ironic/bmc"
	"github.com/metal3-io/baremetal-operator/pkg/provisioner"
	"github.com/metal3-io/baremetal-operator/pkg/utils"
)

type BMCEventSubscriptionReconciler struct {
	client.Client
	Log                logr.Logger
	ProvisionerFactory provisioner.Factory
}

func (r *BMCEventSubscriptionReconciler) Reconcile(ctx context.Context, request ctrl.Request) (result ctrl.Result, err error) {
	reconcileSubscriptionCounters.With(subscriptionMetricLabels(request)).Inc()
	defer func() {
		if err != nil {
			reconcileSubscriptionErrorCounter.Inc()
		}
	}()

	reqLogger := r.Log.WithValues("bmceventsubscription", request.NamespacedName)
	reqLogger.Info("start")

	// Fetch the BMCEventSubscription
	subscription := &metal3v1alpha1.BMCEventSubscription{}
	err = r.Get(ctx, request.NamespacedName, subscription)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Request object not found, could have been deleted after
			// reconcile request.  Owned objects are automatically
			// garbage collected. For additional cleanup logic use
			// finalizers.  Return and don't requeue
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, errors.Wrap(err, "could not load host data")
	}

	if subscription.Spec.HostRef == "" {
		return ctrl.Result{}, errors.New("Missing HostRef")
	}

	host := &metal3v1alpha1.BareMetalHost{}
	namespacedHostName := types.NamespacedName{
		Name:      subscription.Spec.HostRef,
		Namespace: request.Namespace,
	}
	err = r.Get(ctx, namespacedHostName, host)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Request object not found, could have been deleted after
			// reconcile request.  Owned objects are automatically
			// garbage collected. For additional cleanup logic use
			// finalizers.  Return and don't requeue
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, errors.Wrap(err, "could not load host data")
	}

	// Add a finalizer to newly created objects.
	if subscription.DeletionTimestamp.IsZero() && !subscriptionHasFinalizer(subscription) {
		reqLogger.Info(
			"adding finalizer",
			"existingFinalizers", subscription.Finalizers,
			"newValue", metal3v1alpha1.BMCEventSubscriptionFinalizer,
		)
		subscription.Finalizers = append(subscription.Finalizers,
			metal3v1alpha1.BMCEventSubscriptionFinalizer)
		err := r.Update(ctx, subscription)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to add finalizer")
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Retrieve the BMC details from the host spec and validate host
	// BMC details and build the credentials for talking to the
	// management controller.
	var bmcCreds *bmc.Credentials
	var bmcCredsSecret *corev1.Secret

	bmcCreds, bmcCredsSecret, err = r.buildAndValidateBMCCredentials(request, host)
	if err != nil || bmcCreds == nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to get bmc secret")
	}

	initialState := host.Status.Provisioning.State
	info := &reconcileInfo{
		log:            reqLogger.WithValues("provisioningState", initialState),
		host:           host,
		request:        request,
		bmcCredsSecret: bmcCredsSecret,
	}

	prov, err := r.ProvisionerFactory.NewProvisioner(provisioner.BuildHostData(*host, *bmcCreds), info.publishEvent)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to create provisioner")
	}

	ready, err := prov.IsReady()
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to check services availability")
	}
	if !ready {
		reqLogger.Info("provisioner is not ready", "RequeueAfter:", provisionerNotReadyRetryDelay)
		return ctrl.Result{Requeue: true, RequeueAfter: provisionerNotReadyRetryDelay}, nil
	}

	httpHeaders, err := r.getHTTPHeaders(*subscription)

	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to get http headers")
	}

	if subscription.DeletionTimestamp.IsZero() {
		// Not being deleted
		if subscription.Status.SubscriptionID == "" {
			if _, err := prov.AddBMCEventSubscriptionForNode(subscription, httpHeaders); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "failed to add a subscription")
			}

			// Update the subscription with the new ID
			err := r.Update(ctx, subscription)
			if err != nil {
				return ctrl.Result{}, errors.Wrap(err, "failed to update subscription status")
			}

		} else {
			// Update an existing subscription by recreating it in the BMC

			originalSubscription := subscription.DeepCopy()

			if _, err := prov.AddBMCEventSubscriptionForNode(subscription, httpHeaders); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "failed to update subscription")
			}

			if _, err := prov.RemoveBMCEventSubscriptionForNode(*originalSubscription); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "failed to remove a subscription")
			}

			return ctrl.Result{}, nil
		}
	} else {
		// being deleted
		if subscriptionHasFinalizer(subscription) {
			if _, err := prov.RemoveBMCEventSubscriptionForNode(*subscription); err != nil {
				return ctrl.Result{}, errors.Wrap(err, "failed to remove a subscription")
			}

			// Remove finalizer to allow deletion
			subscription.Finalizers = utils.FilterStringFromList(
				subscription.Finalizers, metal3v1alpha1.BMCEventSubscriptionFinalizer)
			info.log.Info("cleanup is complete, removed finalizer",
				"remaining", subscription.Finalizers)
			if err := r.Update(context.Background(), subscription); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	return

}

func (r *BMCEventSubscriptionReconciler) getBMCSecret(request ctrl.Request, host *metal3v1alpha1.BareMetalHost) (bmcCredsSecret *corev1.Secret, err error) {
	if host.Spec.BMC.CredentialsName == "" {
		return nil, &EmptyBMCSecretError{message: "The BMC secret reference is empty"}
	}
	secretKey := host.CredentialsKey()
	bmcCredsSecret = &corev1.Secret{}
	err = r.Get(context.TODO(), secretKey, bmcCredsSecret)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, &ResolveBMCSecretRefError{message: fmt.Sprintf("The BMC secret %s does not exist", secretKey)}
		}
		return nil, err
	}

	return bmcCredsSecret, nil
}

func (r *BMCEventSubscriptionReconciler) getHTTPHeaders(subscription metal3v1alpha1.BMCEventSubscription) ([]map[string]string, error) {
	headers := []map[string]string{}

	if subscription.Spec.HTTPHeadersRef == nil {
		return headers, nil
	}

	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      subscription.Spec.HTTPHeadersRef.Name,
		Namespace: subscription.Spec.HTTPHeadersRef.Namespace,
	}

	err := r.Get(context.TODO(), secretKey, secret)

	if err != nil {
		return headers, err
	}

	for headerName, headerValueBytes := range secret.Data {
		header := map[string]string{}
		header[headerName] = string(headerValueBytes)
		headers = append(headers, header)
	}

	return headers, err
}

func (r *BMCEventSubscriptionReconciler) buildAndValidateBMCCredentials(request ctrl.Request, host *metal3v1alpha1.BareMetalHost) (bmcCreds *bmc.Credentials, bmcCredsSecret *corev1.Secret, err error) {
	// Retrieve the BMC secret from Kubernetes for this host
	bmcCredsSecret, err = r.getBMCSecret(request, host)
	if err != nil {
		return nil, nil, err
	}

	// Check for a "discovered" host vs. one that we have all the info for
	// and find empty Address or CredentialsName fields
	if host.Spec.BMC.Address == "" {
		return nil, nil, &EmptyBMCAddressError{message: "Missing BMC connection detail 'Address'"}
	}

	bmcCreds = credentialsFromSecret(bmcCredsSecret)

	// Verify that the secret contains the expected info.
	err = bmcCreds.Validate()
	if err != nil {
		return nil, bmcCredsSecret, err
	}

	return bmcCreds, bmcCredsSecret, nil
}

// SetupWithManager registers the reconciler to be run by the manager
func (r *BMCEventSubscriptionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&metal3v1alpha1.BMCEventSubscription{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

func (r *BMCEventSubscriptionReconciler) subscriptionHasStatus(subscription *metal3v1alpha1.BMCEventSubscription) bool {
	return !subscription.Status.LastUpdated.IsZero()
}

func subscriptionHasFinalizer(subscription *metal3v1alpha1.BMCEventSubscription) bool {
	return utils.StringInList(subscription.Finalizers, metal3v1alpha1.BMCEventSubscriptionFinalizer)
}
