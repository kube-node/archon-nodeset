/*
Copyright 2017 The Archon Authors.

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

package nodeset

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/pkg/api/v1"
	extensions "k8s.io/kubernetes/pkg/apis/extensions/v1beta1"
	archonclientset "kubeup.com/archon/pkg/clientset"

	"github.com/kube-node/nodeset/pkg/client/clientset_v1alpha1"
	"github.com/kube-node/nodeset/pkg/nodeset/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"kubeup.com/archon/pkg/cluster"
)

var (
	ArchonNodeSetControllerKey = "archon.nodeset.k8s.io"
	AnnotationPrefix           = "archon.nodeset.k8s.io/"
	NodeSetResourceVersionKey  = AnnotationPrefix + "nodeset-resource-version"
	NSIGAnnotationKey          = AnnotationPrefix + "nodeset-instance-group"
)

func getDesiredSelectors(ns *v1alpha1.NodeSet) map[string]string {
	ret := make(map[string]string)
	for k, v := range ns.Spec.NodeSelector {
		ret[k] = v
	}
	return ret
}

func getDesiredLabels(ns *v1alpha1.NodeSet) map[string]string {
	ret := make(map[string]string)
	for k, v := range ns.Labels {
		ret[k] = v
	}
	return ret
}

func getDesiredAnnotations(ns *v1alpha1.NodeSet) map[string]string {
	ret := make(map[string]string)
	for k, v := range ns.Annotations {
		ret[k] = v
	}

	// Annotate this. So later we know if the NodeSet has beed updated or not
	ret[NodeSetResourceVersionKey] = ns.ResourceVersion
	return ret
}

func decodeAndMergeArchonNodeConfig(raw runtime.RawExtension, base *ArchonNodeConfig) (err error) {
	if len(raw.Raw) == 0 {
		return
	}

	old := base.Configs
	err = json.Unmarshal(raw.Raw, base)
	if err != nil {
		return
	}

	index := make(map[string]*cluster.ConfigSpec)
	for _, section := range old {
		index[section.Name] = &section
	}

	// JSON unmarshaler will overwrite base.Configs if it exists in raw. But we
	// want it to merge.
	new := base.Configs
	for _, section := range new {
		if oldSection, ok := index[section.Name]; ok {
			for k, v := range section.Data {
				oldSection.Data[k] = v
			}
		} else {
			old = append(old, section)
		}
	}

	base.Configs = old
	return
}

func classifyArchonNodeResources(namespace string, resources []v1alpha1.NodeClassResource) (users, secrets []cluster.LocalObjectReference, files []cluster.FileSpec, err error) {
	secretGVK := fmt.Sprintf(v1.SchemeGroupVersion.String(), "/", "Secret")
	userGVK := fmt.Sprintf(cluster.SchemeGroupVersion.String(), "/", "User")
	for _, r := range resources {
		if r.Type == v1alpha1.NodeClassResourceFile {
			files = append(files, cluster.FileSpec{
				Name:               r.Name,
				Path:               r.Path,
				Owner:              r.Owner,
				RawFilePermissions: r.Permission,
				Template:           r.Template,
			})
		} else if r.Type == v1alpha1.NodeClassResourceReference {
			if r.Reference == nil {
				err = fmt.Errorf("Nil reference in NodeClassResource %v", r.Name)
				return
			}

			if r.Reference.Namespace != "" && r.Reference.Namespace != namespace {
				err = fmt.Errorf("NodeClassResource reference %v has to be in the same namespace with NodeSets which is %v", r.Name, namespace)
				return
			}

			// Supported references
			// TODO: Specific type of secrets as user. Maybe it should be in Archon
			gvk := fmt.Sprintf(r.Reference.APIVersion, "/", r.Reference.Kind)
			switch gvk {
			case secretGVK:
				secrets = append(secrets, cluster.LocalObjectReference{r.Reference.Name})
				break
			case userGVK:
				users = append(users, cluster.LocalObjectReference{r.Reference.Name})
			}
		} else {
			utilruntime.HandleError(fmt.Errorf("Unknown resource type: %v. Will ignore", r.Type))
		}
	}

	return
}

type checker func() error

func EnsureNodeSetResources(kubeClient archonclientset.Interface, nodesetClient clientset_v1alpha1.Interface, timeout time.Duration) (err error) {
	glog.Infof("Ensuring NodeSet resources")

	return wait.Poll(3*time.Second, timeout, func() (bool, error) {
		return ensureResources(kubeClient, nodesetClient)
	})
}

func ensureResources(kubeClient archonclientset.Interface, nodesetClient clientset_v1alpha1.Interface) (done bool, err error) {
	data := map[string]checker{
		"node-class": func() error {
			_, err = nodesetClient.NodesetV1alpha1().NodeClasses().List(metav1.ListOptions{})
			return err
		},
		"node-set": func() error {
			_, err = nodesetClient.NodesetV1alpha1().NodeSets().List(metav1.ListOptions{})
			return err
		},
	}

	for name, check := range data {
		err = check()

		if errors.IsNotFound(err) {
			tpr := extensions.ThirdPartyResource{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ThirdPartyResource",
					APIVersion: "v1/betav1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("%s.%s", name, cluster.GroupName),
				},
				Versions: []extensions.APIVersion{
					extensions.APIVersion{
						Name: cluster.GroupVersion,
					},
				},
			}

			_, err = kubeClient.Extensions().ThirdPartyResources().Create(&tpr)
			if err != nil {
				return
			}

			glog.Infof("Resource %s is created", name)
		} else if err != nil {
			return
		}
	}

	glog.Infof("All NodeSet resources have been ensured")
	return true, nil
}
