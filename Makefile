# use static label for skaffold to prevent rolling all gardener components on every `skaffold` invocation
up: export SKAFFOLD_LABEL = skaffold.dev/run-id=cilium-lb

up:
	skaffold run --tail --cleanup=false

