**webhook:** the wildcard-egress admission policy re-judges an update only when
the egress rules or the opt-in annotation change, as the 0.17.22 upgrade note
says. It compared the whole spec, so a server stored before the policy, with
wildcard egress and no opt-in, was refused any spec change, including the
endpoint or image the discovery controller rewrites on resync
