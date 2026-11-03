#!/bin/sh
# Shallow clone every open source AI gateway into reference/ for comparison against nebu
set -eu
cd "$(dirname "$0")/.."
mkdir -p reference
printf 'module reference\n' > reference/go.mod
sync_one() {
  meta=$(gh api "repos/$1" --jq '[.pushed_at[:4], .archived] | @tsv')
  year=${meta%	*}
  archived=${meta#*	}
  name=${2:-${1#*/}}
  [ "$archived" = true ] && name="${name}_ARCHIVED"
  dir="reference/$year/$name"
  for old in reference/*/"${2:-${1#*/}}" reference/*/"${2:-${1#*/}}_ARCHIVED"; do
    [ -d "$old/.git" ] && [ "$old" != "$dir" ] && mkdir -p "reference/$year" && mv "$old" "$dir"
  done
  if [ -d "$dir/.git" ]; then
    git -C "$dir" fetch --quiet --depth 1 origin HEAD && git -C "$dir" reset --quiet --hard FETCH_HEAD
  else
    mkdir -p "reference/$year"
    git clone --quiet --depth 1 --single-branch "https://github.com/$1.git" "$dir"
  fi
  echo "synced $1 -> $dir"
}
repos='
BerriAI/litellm
Portkey-AI/gateway portkey-gateway
maximhq/bifrost
tensorzero/tensorzero
Helicone/helicone
Helicone/ai-gateway helicone-ai-gateway
theagentrouter/agent-router
Kong/kong
apache/apisix
api7/aisix
higress-group/higress
kgateway-dev/kgateway
agentgateway/agentgateway
vllora/vllora
katanemo/plano
theopenco/llmgateway
mnfst/llm-gateway
songquanpeng/one-api
QuantumNous/new-api
mlflow/mlflow
TykTechnologies/tyk
TheLunarCompany/lunar
IBM/mcp-context-forge
pydantic/pydantic-ai-gateway
braintrustdata/braintrust-proxy
mozilla-ai/otari
looplj/axonhub
ENTERPILOT/GoModel
tbphp/gpt-load
coaidev/coai
APIParkLab/APIPark
cheaper-inference/OmniRoute
decolua/9router
lidge-jun/opencodex
router-for-me/CLIProxyAPI
musistudio/claude-code-router
BlockRunAI/ClawRouter
weave-os/router weave-router
respanai/respan-gateway
ferro-labs/ai-gateway ferro-ai-gateway
EinStack/glide
bestruirui/octopus
vllm-project/semantic-router
kubernetes-sigs/gateway-api-inference-extension
lm-sys/RouteLLM
llm-d/llm-d
kubeai-project/kubeai
smg-project/smg
thushan/olla
lordmathis/llamactl
yym68686/uni-api
Azure-Samples/AI-Gateway azure-ai-gateway
'
while read -r repo dir; do
  [ -n "$repo" ] || continue
  sync_one "$repo" "$dir" &
  while [ "$(jobs -p | wc -l)" -ge "${JOBS:-6}" ]; do sleep 1; done
done <<REPOS
$repos
REPOS
wait
find reference -mindepth 1 -maxdepth 1 -type d -empty -delete
