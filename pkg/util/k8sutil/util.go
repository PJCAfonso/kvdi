package k8sutil

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/tinyzimmer/kvdi/pkg/apis/kvdi/v1alpha1"
	"github.com/tinyzimmer/kvdi/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LookupClusterByName fetches the VDICluster with the given name
func LookupClusterByName(c client.Client, name string) (*v1alpha1.VDICluster, error) {
	found := &v1alpha1.VDICluster{}
	return found, c.Get(context.TODO(), types.NamespacedName{Name: name, Namespace: metav1.NamespaceAll}, found)
}

// IsMarkedForDeletion returns true if the given cluster is marked for deletion.
func IsMarkedForDeletion(cr *v1alpha1.VDICluster) bool {
	return cr.GetDeletionTimestamp() != nil
}

// SetCreationSpecAnnotation sets an annotation with a checksum of the desired
// spec of the object.
func SetCreationSpecAnnotation(meta *metav1.ObjectMeta, obj runtime.Object) error {
	annotations := meta.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	spec, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := h.Write(spec); err != nil {
		return err
	}
	annotations[v1.CreationSpecAnnotation] = fmt.Sprintf("%x", h.Sum(nil))
	meta.SetAnnotations(annotations)
	return nil
}

// CreationSpecsEqual returns true if the two objects spec annotations are equal.
func CreationSpecsEqual(m1 metav1.ObjectMeta, m2 metav1.ObjectMeta) bool {
	m1ann := m1.GetAnnotations()
	m2ann := m2.GetAnnotations()
	spec1, ok := m1ann[v1.CreationSpecAnnotation]
	if !ok {
		return false
	}
	spec2, ok := m2ann[v1.CreationSpecAnnotation]
	if !ok {
		return false
	}
	return spec1 == spec2
}

func GetThisPodName() (string, error) {
	if podName := os.Getenv("POD_NAME"); podName != "" {
		return podName, nil
	}
	return "", errors.New("No POD_NAME in the environment")
}

func GetThisPodNamespace() (string, error) {
	if podNS := os.Getenv("POD_NAMESPACE"); podNS != "" {
		return podNS, nil
	}
	return "", errors.New("No POD_NAMESPACE in the environment")
}

func GetThisPod(c client.Client) (*corev1.Pod, error) {
	podName, err := GetThisPodName()
	if err != nil {
		return nil, err
	}
	podNamespace, err := GetThisPodNamespace()
	if err != nil {
		return nil, err
	}
	nn := types.NamespacedName{Name: podName, Namespace: podNamespace}
	pod := &corev1.Pod{}
	return pod, c.Get(context.TODO(), nn, pod)
}
