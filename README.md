# Dapr Pluggable Components Operator

A custom Kubernetes Pod Controller for automatically inject [Dapr](https://dapr.io/) pluggable components in your pods.

# TLDR

## Step 1: Install the Operator

## Step 2: Annotate your deployments

Your deployment annotations tells if the pluggable components operator should take your pod and inject the containers or not. This is achieved by annotating them with `components.dapr.io/enabled: "true"`. That way your deployments the operator will consider your deployment eligible to inject component containers.

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

Your component is the entrypoint for defining container images for pluggable components, you must set the `components.dapr.io/image` annotation pointing to the desired image that should be used for the pluggable component container.

```yaml
apiVersion: dapr.io/v1alpha1
kind: Component
metadata:
  name: redis-pluggable
  annotations:
    components.dapr.io/image: "DOCKER_REPO/state-redis:latest" # required
spec:
  type: state.redis-pluggable
  initTimeout: 1m
  version: v1
  metadata:
    - name: redisHost
      value: redis-svc.default.svc.cluster.local:6379
    - name: redisPassword
      value: ""
    - name: processingTimeout
      value: 1m
    - name: redeliverInterval
      value: 10s
    - name: idleCheckFrequency
      value: 5s
    - name: readTimeout
      value: 5s
```

# Contributing

## Dependencies

1. [Operator-sdk](https://sdk.operatorframework.io/docs/installation/)
