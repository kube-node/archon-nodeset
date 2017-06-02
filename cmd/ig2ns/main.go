package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/ghodss/yaml"
	"io/ioutil"
	"os"

	nodeset "github.com/kube-node/archon-nodeset/pkg/controller/archon-nodeset"
	"github.com/kube-node/nodeset/pkg/nodeset/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/pkg/api/v1"
	"kubeup.com/archon/pkg/cluster"
)

// Construct a NodeSet from InstanceGroup
func InstanceGroupToNodeClass(ig *cluster.InstanceGroup) (nc *v1alpha1.NodeClass, err error) {
	tpl := ig.Spec.Template
	nodeConfig := &nodeset.ArchonNodeConfig{
		OS:                       tpl.Spec.OS,
		Image:                    tpl.Spec.Image,
		InstanceType:             tpl.Spec.InstanceType,
		NetworkName:              tpl.Spec.NetworkName,
		Hostname:                 tpl.Spec.NetworkName,
		Configs:                  tpl.Spec.Configs,
		Annotations:              tpl.ObjectMeta.Annotations,
		MinReadySeconds:          ig.Spec.MinReadySeconds,
		ReservedInstanceSelector: ig.Spec.ReservedInstanceSelector,
		ReclaimPolicy:            tpl.Spec.ReclaimPolicy,
		ProvisionPolicy:          ig.Spec.ProvisionPolicy,
	}
	configData, err := json.Marshal(nodeConfig)

	resources := mergeArchonNodeResources(&tpl)
	if err != nil {
		err = fmt.Errorf("Unable to merge resources from InstanceGroup %v: %v", nc.Name, err)
		return
	}

	nc = &v1alpha1.NodeClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.SchemeGroupVersion.String(),
			Kind:       "NodeClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        ig.Name,
			Labels:      ig.Labels,
			Annotations: ig.Annotations,
			Namespace:   v1alpha1.TPRNamespace,
		},
		Config:    runtime.RawExtension{Raw: configData},
		Resources: resources,
	}

	return
}

func mergeArchonNodeResources(tpl *cluster.InstanceTemplateSpec) (ret []v1alpha1.NodeClassResource) {
	for _, u := range tpl.Spec.Users {
		ret = append(ret, v1alpha1.NodeClassResource{
			Type: v1alpha1.NodeClassResourceReference,
			Name: u.Name,
			Reference: &v1.ObjectReference{
				Name:       u.Name,
				Kind:       "User",
				Namespace:  tpl.Namespace,
				APIVersion: cluster.SchemeGroupVersion.String(),
			},
		})
	}

	for _, u := range tpl.Spec.Secrets {
		ret = append(ret, v1alpha1.NodeClassResource{
			Type: v1alpha1.NodeClassResourceReference,
			Name: u.Name,
			Reference: &v1.ObjectReference{
				Name:       u.Name,
				Kind:       "Secret",
				Namespace:  tpl.Namespace,
				APIVersion: v1.SchemeGroupVersion.String(),
			},
		})
	}

	for _, u := range tpl.Spec.Files {
		f := v1alpha1.NodeClassResource{
			Type:       v1alpha1.NodeClassResourceFile,
			Name:       u.Name,
			Path:       u.Path,
			Permission: u.RawFilePermissions,
			Owner:      u.Owner,
			Template:   u.Template,
		}
		if f.Template == "" && u.Content != "" {
			f.Template = u.Content
		}
		ret = append(ret, f)
	}

	return
}

func main() {
	defer utilruntime.HandleCrash()

	flag.Parse()

	// Read file
	if len(flag.Args()) == 0 {
		fmt.Printf("Need a file name")
		os.Exit(1)
	}

	data, err := ioutil.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Printf("Unable to read file: %v", err)
		os.Exit(2)
	}

	ig := &cluster.InstanceGroup{}
	err = yaml.Unmarshal(data, ig)
	if err != nil {
		fmt.Printf("Unable to decode file: %v", err)
		os.Exit(3)
	}

	if ig.Kind != "InstanceGroup" || ig.APIVersion != cluster.SchemeGroupVersion.String() {
		fmt.Printf("Unsupported kind or apiVersion: %+v", ig.ObjectMeta)
		os.Exit(4)
	}

	nc, err := InstanceGroupToNodeClass(ig)
	if err != nil {
		fmt.Printf("Unable to convert InstanceGroup: %v", err)
		os.Exit(5)
	}

	data, err = yaml.Marshal(nc)
	if err != nil {
		fmt.Printf("Unable to marshal NodeClass: %v", err)
		os.Exit(6)
	}

	fmt.Printf("%s", string(data))
}
