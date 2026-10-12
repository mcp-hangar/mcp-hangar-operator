### owned network policies are written with Server-Side Apply

The operator now writes the NetworkPolicies and CiliumNetworkPolicies it owns with Server-Side Apply, as field manager `mcp-hangar-operator`, and forces ownership of the fields it sets. Before, it used a full Update.

The first reconcile after the upgrade takes over the fields the previous version wrote, so `kubectl get networkpolicy -o yaml --show-managed-fields` lists `mcp-hangar-operator` as their manager from then on. Labels and annotations that other tools add to these policies are no longer removed.

A label the operator itself stops setting in a later version stays on the object until someone removes it. A full Update would have dropped it.

Nothing needs to change in your manifests or values.
