# Dapr Pluggable Components Operator

A custom Kubernetes Pod Controller for automatically inject [Dapr](https://dapr.io/) pluggable components containers based on declared Dapr components.

# TLDR

## Step 1: Install the Operator

```shell
helm repo add my-repo https://mcandeia.github.io/dapr-pluggable-components-operator
helm install my-release my-repo/dapr-pluggable-components-operator
```

## Step 2: Annotate your deployments

Your deployment annotations tells if the pluggable components operator should take your pod and inject the containers or not. This is achieved by annotating them with `components.dapr.io/enabled: "true"`. That way the operator considers your deployment eligible to inject component containers.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
  labels:
    app: app
spec:
  replicas: 1
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
      annotations:
        components.dapr.io/enabled: "true" # required
        dapr.io/app-id: "my-app"
        dapr.io/enabled: "true"
    spec:
      containers:
        - name: "my-app"
          image: "MY_APP_IMAGE"
```

## Step 3: Annotate your components

Your component is the entrypoint for defining container images for pluggable components, you must set the `components.dapr.io/container-image` annotation pointing to the desired image that should be used for the pluggable component container.

```yaml
apiVersion: dapr.io/v1alpha1
kind: Component
metadata:
  name: jsonlogic
  annotations:
    components.dapr.io/container-image: "ghcr.io/mcandeia/dapr-components:latest"
spec:
  type: bindings.jsonlogic
  version: v1
  metadata: []
```

Optionally you can mount volumes and add env variables into the containers by using the `components.dapr.io/container-volume-mounts` and `components.dapr.io/container-env` annotations and use `;` to separate the volume name and its path and `,` to separate volume mount.

```yaml
apiVersion: dapr.io/v1alpha1
kind: Component
metadata:
  name: jsonlogic
  annotations:
    components.dapr.io/container-image: "ghcr.io/mcandeia/dapr-components:latest" #required
    components.dapr.io/container-volume-mounts: "volume-name;/volume-path,volume-name-2;/volume-path-2" # optional
    components.dapr.io/container-env: "env-var;env-var-value,env-var-2;env-var-value-2" #optional
spec:
  type: bindings.jsonlogic
  version: v1
  metadata: []
```

By default the operator creates undeclared volumes as `emptyDir` volumes, if you want a different volume type you should declare it by yourself in your deploments.

# Contributing

## Dependencies

1. [Operator-sdk](https://sdk.operatorframework.io/docs/installation/)
