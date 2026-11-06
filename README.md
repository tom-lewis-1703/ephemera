# ephemera

Give every AI red team run its own disposable Kubernetes sandbox. The run attacks the model, the report comes out, and the sandbox gets deleted.

That's the goal, anyway. Right now the operator's API type exists but doesn't do anything yet. See [Status](#status) for exactly where things stand.

## Why I'm building this

Red teaming a model means attacking it on purpose to find its weaknesses. If you do that on shared infrastructure, one run can affect the next, and the target can end up sitting near things it shouldn't be able to reach, like your API keys. A fresh sandbox for each run avoids both problems, and because the environment is defined in code, you can recreate it for a later run.

This started as a preview environment tool: open a PR, get a live environment, close it, and the environment goes away. I switched to red teaming because the same pattern fits, and isolation matters more when the thing inside the environment is being attacked. I'm building it as a personal project during my senior year of college.

It's also my excuse to learn how operators and Kubernetes actually fit together.

## How it will work

You create a `RedTeamRun` object, and the operator handles the rest:

1. Create a new namespace for the run
2. Start the target model in it
3. Run [garak](https://github.com/NVIDIA/garak), an open-source tool that attacks a model and reports the results, as a Job
4. Save the report outside the namespace
5. Delete the namespace and everything in it

None of this is built yet. The first version will use a small open-weight model that runs on a CPU.

A namespace on its own doesn't isolate much, so the sandbox will also need network rules that block traffic by default, along with tests that check those rules hold. Model output can vary between runs too, so repeatable here means pinned versions and recorded settings, with results reported as success rates.

## Status

**Phase 0: prove the routing by hand. Done.**

This came from the preview environment design and is the only part that runs today.

- kind cluster with host port mappings
- Envoy Gateway with a shared Gateway, exposed through a NodePort
- A hand-written `whoami` app that returns a 200 through the Gateway at its own hostname

**Phase 1: write the operator. In progress.**

- Add a custom resource: done, as `RedTeamRun`, with a target image and a list of garak probes. The spec can't change after a run is created
- Have the reconciler create the sandbox, run garak, save the report and clean up: not started

**After that:**

- Lock down the sandbox and write tests that try to break out of it
- Test AI agents using fake tools and fake credentials
- Trigger runs from pull requests

The controller's `Reconcile` function is still empty, so creating a `RedTeamRun` doesn't do anything yet.

## Try it locally

This runs the Phase 0 routing setup on your own machine. It doesn't involve the operator or any models. I develop on WSL 2 with Ubuntu 24.04, so the commands are written for a bash shell. Run them from the root of the repo.

### Prerequisites

The versions in parentheses are what I'm running.

- [Docker](https://docs.docker.com/get-docker/)
- [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) (v0.32.0, which runs Kubernetes v1.36.1)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Helm](https://helm.sh/docs/intro/install/) (v4.2.3)
- Port 80 and 443 free on your machine, since kind binds to both
- Go 1.26+ if you want to build the operator scaffold (1.26.5)

### 1. Create the cluster

```sh
kind create cluster --config hack/kind-config.yaml
```

### 2. Install Envoy Gateway

```sh
helm install eg oci://docker.io/envoyproxy/gateway-helm --version v1.8.3 -n envoy-gateway-system --create-namespace
kubectl wait --for=condition=Available deployment/envoy-gateway -n envoy-gateway-system --timeout=5m
```

It has to go in the `envoy-gateway-system` namespace, because the `EnvoyProxy` config in `hack/gateway.yaml` lives there.

### 3. Create the Gateway

```sh
kubectl apply -f hack/gateway.yaml
kubectl wait --for=condition=Programmed gateway/ephemera -n ephemera-system --timeout=120s
```

### 4. Deploy the test app

```sh
kubectl apply -f hack/hello-world.yaml
kubectl rollout status deployment/whoami -n hello
```

### 5. Hit it

```sh
curl http://hello.127.0.0.1.nip.io
```

You should get a 200 back from the `whoami` pod, with details about the request. [nip.io](https://nip.io) is a public DNS service that resolves that hostname to `127.0.0.1`, so there's no hosts file to edit, but you do need internet access for the lookup.

This is the path that request takes:

```text
curl hello.127.0.0.1.nip.io
  -> port 80 on the host
  -> port 30080 on the kind node
  -> Envoy proxy (Gateway "ephemera")
  -> HTTPRoute in the preview namespace
  -> Service
  -> Pod
```

### Clean up

```sh
kind delete cluster --name ephemera
```

## Things I learned the hard way

**A Gateway can be accepted and still not work.** My Gateway sat at `Programmed=False` because Envoy Gateway creates its proxy Service as a `LoadBalancer` by default. kind has no load balancer provider, so that Service never gets an address. I switched it to a NodePort on 30080 to line up with the port mapping in the kind config.

**Check the controller logs, not just the status.** My first attempt at the NodePort patch was wrong, and the GatewayClass kept reporting `Accepted=True` the whole time. The error only showed up in the `envoy-gateway` logs. The cause was that the merge key for Service ports is `port`, not `name`, so the patch has to include `port`.

**What you apply isn't always what you get back.** `kubectl get httproute -o yaml` shows more than `hack/hello-world.yaml` contains, because `matches` defaults to `PathPrefix /` and `backendRefs` defaults to weight 1. Don't expect the two to match. That's worth remembering for the reconciler too, since a naive comparison against the live object would always see a difference.

## Project layout

| Path | What's in it |
| --- | --- |
| `hack/kind-config.yaml` | Local cluster and host port mappings |
| `hack/gateway.yaml` | Namespace, EnvoyProxy config, GatewayClass and Gateway, from the preview design |
| `hack/hello-world.yaml` | The manual routing smoke test, from the preview design |
| `api/v1alpha1/` | The `RedTeamRun` type (Kubebuilder scaffold) |
| `internal/controller/` | The `RedTeamRun` controller, with `Reconcile` still empty (Kubebuilder scaffold) |
| `cmd/main.go` | Operator entry point (Kubebuilder scaffold) |
| `config/` | Kustomize manifests for deploying the operator (Kubebuilder scaffold) |
| `test/` | End-to-end test setup (Kubebuilder scaffold) |
| `.github/workflows/` | Lint, test and e2e workflows (Kubebuilder scaffold) |

## Development

These come from the Kubebuilder Makefile and need a bash shell. Run `make help` to see the rest.

```sh
make build     # build the manager binary
make lint      # run golangci-lint
make test      # run unit tests
make test-e2e  # run e2e tests in a throwaway kind cluster
```

## Built with

Go, [Kubebuilder](https://book.kubebuilder.io/), [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime), [Envoy Gateway](https://gateway.envoyproxy.io/), the [Gateway API](https://gateway-api.sigs.k8s.io/), [Helm](https://helm.sh/) and [kind](https://kind.sigs.k8s.io/).

## Contributing

This is a personal project and I'm mostly building it to learn, so I'm not really set up for contributions yet. If you spot something wrong or have a suggestion though, please open an issue. I'd like to hear it.

## License

Copyright 2026 Thomas Lewis.

Licensed under the Apache License, Version 2.0. You may obtain a copy of the license at <http://www.apache.org/licenses/LICENSE-2.0>.
