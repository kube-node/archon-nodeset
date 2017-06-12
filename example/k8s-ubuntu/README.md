NodeSet Example with Ubuntu and Kubeadm
==========================================

In this guide, we'll demonstrate how to create a NodeSet with
ubuntu using [kubeadm].

Step 1
------

First, please follow the [installation guide] to install `archon-controller`
locally or into your Kubernetes cluster.


Step 2
------

Please install `archon-nodeset` and make sure it runs in your cluster.

If we wish to use another namespace other than `default`, pass `--instance-group-namespace=your_namespace` to
 `archon-nodeset`.

Step 3
------

Modify `k8s-user.yaml`. Replace `YOUR_SSH_KEY` with your public key which will be
used for authentication with the server. And create the user resource.

```
kubectl create -f k8s-user.yaml --namespace=your_namespace_or_default
```

Step 4
------

You can skip this step if your network is created with Archon.

Modify `k8s-net.yaml`. Replace everything with the real values. Then create it.

```
kubectl create -f k8s-net.yaml --namespace=your_namespace_or_default
```

Step 5
------

Create a bootstrap token if you don't have one. See [kubeadm guide].

Step 6
------

Replace `TOKEN` and `MASTER_IP` in the `k8s-nodeclass.yaml` file.
Then create the nodeclass with:

```
kubectl create -f k8s-nodeclass.yaml --namespace=your_namespace_or_default
```

Step 7
------

Create your nodeset:

```
kubectl create -f k8s-nodeset.yml --namespace=your_namespace_or_default
```


[installation guide]: https://github.com/kubeup/archon#installation
[kubeadm]: https://kubernetes.io/docs/getting-started-guides/kubeadm/
[kubeadm guide]: https://kubernetes.io/docs/admin/bootstrap-tokens/#token-management-with-kubeadm
