#!/bin/bash

source ~/.env

CLUSTER_NAME=$1

kind get kubeconfig > ~/.kube/config
clusterctl get kubeconfig ${CLUSTER_NAME} >  ~/.kube/${CLUSTER_NAME}-config
cp ~/.kube/${CLUSTER_NAME}-config  ~/.kube/config

kubectl create namespace hbang-resources
kubectl create namespace hbang-argocd
kubectl create namespace hbang-prometheus
kubectl create namespace hbang-ejbca

helm repo add cilium https://helm.cilium.io
helm upgrade --install cilium cilium/cilium --namespace kube-system --version 1.18.1 --force

helm repo add argo https://argoproj.github.io/argo-helm
helm upgrade --install argocd argo/argo-cd --namespace hbang-argocd --force

curl --request GET \
    --url 'https://us.infisical.com/api/v3/secrets/raw/seed-secret?secretPath=%2F&type=shared&viewSecretValue=true&expandSecretReferences=false&include_imports=false&workspaceId=ecaf1872-72fa-4c99-908f-16acf09f5eeb&environment=dev' \
    --header 'Authorization: Bearer $INFISICAL_TOKEN' | jq -r .secret.secretValue | kubectl apply -f -

helm template helm/apps | kubectl apply -f -
