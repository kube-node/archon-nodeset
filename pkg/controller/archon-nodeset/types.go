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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"kubeup.com/archon/pkg/cluster"
)

type ArchonNodeConfig struct {
	OS           string `json:"os,omitempty"`
	Image        string `json:"image,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	NetworkName  string `json:"networkName,omitempty"`
	Hostname     string `json:"hostname,omitempty"`

	MinReadySeconds          int32                                `json:"minReadySeconds,omitempty"`
	ReservedInstanceSelector *metav1.LabelSelector                `json:"reservedInstaceSelector,omitempty"`
	ReclaimPolicy            cluster.InstanceReclaimPolicy        `json:"reclaimPolicy,omitempty"`
	ProvisionPolicy          cluster.InstanceGroupProvisionPolicy `json:"provisionPolicy,omitempty"`

	// Render context of file templates
	Configs []cluster.ConfigSpec `json:"configs,omitempty"`
	// Annotations to be put on every Instance created
	Annotations map[string]string `json:"annotations,omitempty"`
}
