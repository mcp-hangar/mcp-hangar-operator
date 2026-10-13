### `--webhook-annotation-rules` and `--image-digest-policy` are gone

Both flags did nothing since 0.17.24. They are now unknown, so an operator
started with either one exits at startup. Chart 0.12.40 and newer no longer
pass them; remove them from any manifest or `operator.extraArgs` that does.
Set the image digest level as `imageDigestPolicy` in the admission policy
params ConfigMap (the chart's `operator.imageDigestPolicy`).
