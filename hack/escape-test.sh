#!/usr/bin/env bash
# Tries to break out of a red team sandbox, and checks the paths a run needs
# still work. Needs the operator running (deployed or make run) and both images loaded.
#
# It starts two runs that keep their sandboxes, plus a decoy service outside
# any sandbox, then connects from inside sandbox A as:
#   - a pod with garak's labels, standing in for a compromised harness
#   - the target model's own pod, standing in for a compromised model
#   - an unlabeled pod, standing in for anything else in the sandbox
#
# Every connection ends up as one of:
#   open     the connection succeeded
#   closed   it timed out or the network was unreachable, which is what a
#            NetworkPolicy drop looks like
#   refused  something answered and refused it, so the packet got through
#   error    the probe itself failed, such as kubectl exec erroring
# Only "closed" counts as blocked. Exits nonzero if any check doesn't match
# what it expects, including any probe error.
set -u

TARGET_IMAGE=${TARGET_IMAGE:-ephemera-target:qwen2.5-0.5b}
HARNESS_IMAGE=${HARNESS_IMAGE:-ephemera-harness:garak-0.17.1}
RUN_A=escape-a
RUN_B=escape-b
NS_A=redteam-$RUN_A
NS_B=redteam-$RUN_B
DECOY_NS=escape-decoy

cleanup() {
  kubectl delete redteamrun "$RUN_A" "$RUN_B" --ignore-not-found --wait=false >/dev/null
  kubectl delete ns "$DECOY_NS" --ignore-not-found --wait=false >/dev/null
}
trap cleanup EXIT

die() { echo "setup failed: $*" >&2; exit 2; }

start_run() {
  kubectl apply -f - >/dev/null <<EOF || die "couldn't create run $1"
apiVersion: redteam.ephemera.dev/v1alpha1
kind: RedTeamRun
metadata:
  name: $1
  annotations:
    ephemera.dev/keep-sandbox: "true"
spec:
  targetImage: $TARGET_IMAGE
  targetModel: qwen2.5:0.5b
  probes: [dan.Dan_11_0]
EOF
}

# A pod that sleeps so checks can be run in it. The security settings meet
# the restricted Pod Security level, so it can run in a locked-down sandbox.
probe_pod() { # probe_pod <namespace> <name> <labels as JSON>
  kubectl apply -n "$1" -f - >/dev/null <<EOF || die "couldn't create probe pod $1/$2"
{"apiVersion": "v1", "kind": "Pod",
 "metadata": {"name": "$2", "labels": $3},
 "spec": {"automountServiceAccountToken": false,
  "securityContext": {"runAsNonRoot": true, "runAsUser": 1000, "seccompProfile": {"type": "RuntimeDefault"}},
  "containers": [{"name": "probe", "image": "$HARNESS_IMAGE", "imagePullPolicy": "IfNotPresent",
   "command": ["python", "-c", "import time; time.sleep(3600)"],
   "securityContext": {"allowPrivilegeEscalation": false, "capabilities": {"drop": ["ALL"]}}}]}}
EOF
  kubectl wait --for=condition=Ready pod/"$2" -n "$1" --timeout=120s >/dev/null || die "probe pod $1/$2 isn't ready"
}

# Runs in the probe pods. A name that doesn't resolve is an error rather than
# closed, since every name checked here should resolve.
PY_CONNECT='
import errno, socket, sys
host, port = sys.argv[1], int(sys.argv[2])
try:
    family, kind, proto, _, addr = socket.getaddrinfo(host, port, type=socket.SOCK_STREAM)[0]
except socket.gaierror as e:
    print("error:name lookup failed:", e)
    sys.exit(0)
s = socket.socket(family, kind, proto)
s.settimeout(3)
try:
    s.connect(addr)
    print("open")
except TimeoutError:
    print("closed")
except ConnectionRefusedError:
    print("refused")
except OSError as e:
    print("closed" if e.errno in (errno.EHOSTUNREACH, errno.ENETUNREACH) else f"error:{e}")
'

# Runs in the target pod, which has bash but no Python.
BASH_CONNECT='
err=$(timeout 3 bash -c "</dev/tcp/$1/$2" 2>&1); rc=$?
if [ $rc -eq 0 ]; then echo open
elif [ $rc -eq 124 ]; then echo closed
else
  case "$err" in
    *"Connection refused"*) echo refused ;;
    *"No route to host"*|*"Network is unreachable"*) echo closed ;;
    *) echo "error:$err" ;;
  esac
fi
'

# probe <command...> runs a probe and passes its result through, unless the
# command itself failed or printed something unexpected.
probe() {
  local out
  if ! out=$("$@" 2>&1); then
    echo "error:probe command failed: $(tr '\n' ' ' <<<"$out")"
    return
  fi
  case $out in
    open | closed | refused | error:*) echo "$out" ;;
    *) echo "error:unexpected output: $(tr '\n' ' ' <<<"$out")" ;;
  esac
}
from_pod() { probe kubectl exec -n "$1" "$2" -- python -c "$PY_CONNECT" "$3" "$4"; }
from_target() { probe kubectl exec -n "$NS_A" deploy/target -- bash -c "$BASH_CONNECT" _ "$1" "$2"; }

pass=0
fail=0
errors=0
blocked=0
attempts=0
check() { # check <expected open|closed|refused> <actual> <description>
  local expected=$1 actual=$2
  if [[ $expected == closed ]]; then
    attempts=$((attempts + 1))
    [[ $actual == closed ]] && blocked=$((blocked + 1))
  fi
  [[ $actual == error:* ]] && errors=$((errors + 1))
  if [[ $expected == "$actual" ]]; then
    pass=$((pass + 1)); printf '  ok    %-7s %s\n' "$actual" "$3"
  else
    fail=$((fail + 1)); printf '  FAIL  %-7s %s (expected %s)\n' "${actual%%:*}" "$3" "$expected"
    [[ $actual == error:* ]] && printf '        %s\n' "${actual#error:}"
  fi
}

echo "== setting up"
# A previous run's namespaces may still be terminating.
for ns in "$DECOY_NS" "$NS_A" "$NS_B"; do
  kubectl wait --for=delete ns/"$ns" --timeout=180s >/dev/null 2>&1 || die "namespace $ns from an earlier run is still there"
done
kubectl create ns "$DECOY_NS" >/dev/null || die "couldn't create $DECOY_NS"
kubectl create deployment decoy -n "$DECOY_NS" --image="$TARGET_IMAGE" --port=11434 >/dev/null || die "couldn't create the decoy"
kubectl expose deployment decoy -n "$DECOY_NS" --port=11434 >/dev/null || die "couldn't expose the decoy"
start_run "$RUN_A"
start_run "$RUN_B"
for run in "$RUN_A" "$RUN_B"; do
  kubectl wait --for=jsonpath='{.status.phase}'=Succeeded redteamrun/"$run" --timeout=300s >/dev/null ||
    die "run $run didn't succeed: $(kubectl get redteamrun "$run" -o jsonpath='{.status.message}')"
done
kubectl rollout status deployment/decoy -n "$DECOY_NS" --timeout=120s >/dev/null || die "the decoy isn't ready"
probe_pod "$NS_A" harness-probe '{"batch.kubernetes.io/job-name": "garak"}'
probe_pod "$NS_A" other-probe '{"app": "other"}'
probe_pod "$DECOY_NS" decoy-probe '{"app": "decoy-probe"}'

svc_ip() { kubectl get svc "$2" -n "$1" -o jsonpath='{.spec.clusterIP}'; }
TARGET_A=$(svc_ip "$NS_A" target)
TARGET_B=$(svc_ip "$NS_B" target)
DECOY=$(svc_ip "$DECOY_NS" decoy)
API=$(svc_ip default kubernetes)
DECOY_POD=$(kubectl get pods -n "$DECOY_NS" -l app=decoy -o jsonpath='{.items[0].status.podIP}')
NODE=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
for value in TARGET_A TARGET_B DECOY API DECOY_POD NODE; do
  [[ -n ${!value} ]] || die "couldn't look up $value"
done

echo "== controls: the probes work and can tell refusals from drops"
check open "$(from_pod "$NS_A" harness-probe target 11434)" "harness -> its own target model, by name"
check open "$(from_pod "$NS_A" harness-probe "$TARGET_A" 11434)" "harness -> its own target model, by IP"
check open "$(from_target 127.0.0.1 11434)" "target -> itself"
check open "$(from_pod "$DECOY_NS" decoy-probe "$DECOY" 11434)" "outside pod -> decoy, which has no policies"
check refused "$(from_pod "$DECOY_NS" decoy-probe "$DECOY_POD" 9)" "outside pod -> decoy on a port nothing listens on"

echo "== escape attempts from the harness"
check closed "$(from_pod "$NS_A" harness-probe 1.1.1.1 443)" "harness -> internet (1.1.1.1:443)"
check closed "$(from_pod "$NS_A" harness-probe example.com 443)" "harness -> internet by name (example.com:443)"
check closed "$(from_pod "$NS_A" harness-probe "$API" 443)" "harness -> Kubernetes API Service"
check closed "$(from_pod "$NS_A" harness-probe "$NODE" 6443)" "harness -> Kubernetes API on the node"
check closed "$(from_pod "$NS_A" harness-probe "$NODE" 10250)" "harness -> kubelet on the node"
check closed "$(from_pod "$NS_A" harness-probe 169.254.169.254 80)" "harness -> cloud metadata address"
check closed "$(from_pod "$NS_A" harness-probe "$TARGET_B" 11434)" "harness -> another run's target model"
check closed "$(from_pod "$NS_A" harness-probe "$DECOY" 11434)" "harness -> service outside any sandbox"

echo "== escape attempts from the target model"
check closed "$(from_target 1.1.1.1 443)" "target -> internet (1.1.1.1:443)"
check closed "$(from_target "$API" 443)" "target -> Kubernetes API Service"
check closed "$(from_target "$NODE" 10250)" "target -> kubelet on the node"
check closed "$(from_target "$TARGET_B" 11434)" "target -> another run's target model"
check closed "$(from_target "$DECOY" 11434)" "target -> service outside any sandbox"

echo "== other traffic"
check closed "$(from_pod "$NS_A" other-probe "$TARGET_A" 11434)" "other pod in the sandbox -> target model"
check closed "$(from_pod "$NS_A" other-probe 1.1.1.1 443)" "other pod in the sandbox -> internet"
check closed "$(from_pod "$DECOY_NS" decoy-probe "$TARGET_A" 11434)" "pod outside the sandbox -> target model"

echo "== pod security"
# Only a PodSecurity rejection counts as blocked. Any other kubectl error is a probe error.
privileged='{"spec": {"containers": [{"name": "root-probe", "image": "'"$HARNESS_IMAGE"'",
  "command": ["true"], "securityContext": {"privileged": true, "runAsUser": 0}}]}}'
if out=$(kubectl run root-probe -n "$NS_A" --image="$HARNESS_IMAGE" --restart=Never --overrides="$privileged" 2>&1); then
  admitted=open
elif [[ $out == *"violates PodSecurity"* ]]; then
  admitted=closed
else
  admitted="error:$(tr '\n' ' ' <<<"$out")"
fi
check closed "$admitted" "privileged root pod -> admitted to the sandbox"

echo
echo "blocked $blocked of $attempts escape attempts, $pass of $((pass + fail)) checks as expected, $errors probe errors"
[[ $fail -eq 0 ]]
