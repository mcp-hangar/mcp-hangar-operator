**infra:** the operator logs its version and commit at startup, stamped into
the binary by the image build, which is now `-trimpath` with stripped symbols
and digest-pinned base images
