# Archon NodeSet

Archon NodeSet is an implementation of Kubernetes [NodeSet][NodeSet] backed by [Archon][Archon].

## How it works

While supporting the schema of NodeSet, it preserves most of Archon. The concepts in Archon still exist. Behind the scene, the controller will map NodeSet to InstanceGroup on a one-to-one relationship. Archon controllers will pick up from there, create Instances and provision them.

## Usage

Archon NodeSet controller needs to run with Archon controllers.

1. Run Archon controllers [in cluster][3] or [locally][4]
2. Run Archon NodeSet controller `cmd/archon-nodeset/nodeset-controller --kubeconfig path/to/kubeconfig`
3. Create Network. This is required.
4. Create NodeClass and NodeSets 


[NodeSet]: ../../../nodeset
[Archon]: ../../../../kubeup/archon
[3]: ../../../../kubeup/archon#in-cluster-deployment
[4]: ../../../../kubeup/archon#launch-locally


